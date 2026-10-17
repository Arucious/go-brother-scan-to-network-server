package destination

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// FTP stores scans on an FTP or FTPS server.
type FTP struct {
	Config config.Destination
}

func (f *FTP) connect(ctx context.Context) (*ftp.ServerConn, error) {
	c := f.Config
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	opts := []ftp.DialOption{ftp.DialWithContext(ctx), ftp.DialWithTimeout(15 * time.Second)}
	tlsConfig := &tls.Config{ServerName: c.Host, InsecureSkipVerify: c.InsecureSkipVerify} //nolint:gosec // opt-in
	switch c.TLS {
	case "explicit":
		opts = append(opts, ftp.DialWithExplicitTLS(tlsConfig))
	case "implicit":
		opts = append(opts, ftp.DialWithTLS(tlsConfig))
	}
	conn, err := ftp.Dial(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("ftp: connect %s: %w", addr, err)
	}
	if err := conn.Login(c.Username, c.Password); err != nil {
		_ = conn.Quit()
		return nil, fmt.Errorf("ftp: login to %s as %q: %w", addr, c.Username, err)
	}
	return conn, nil
}

// ftpPath joins paths relative to the login directory.
func (f *FTP) ftpPath(parts ...string) string {
	return strings.TrimPrefix(path.Join(append([]string{f.Config.Path}, parts...)...), "/")
}

// mkdirAll creates every missing directory of the absolute path dir, then
// returns to home. MakeDir fails for existing directories, so errors are
// ignored and the result is verified by changing into dir.
func mkdirAll(conn *ftp.ServerConn, dir, home string) error {
	cur := "/"
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		_ = conn.MakeDir(cur)
	}
	if err := conn.ChangeDir(dir); err != nil {
		return fmt.Errorf("ftp: create directory %s: %w", dir, err)
	}
	return conn.ChangeDir(home)
}

func (f *FTP) Save(ctx context.Context, files []File) ([]Saved, error) {
	conn, err := f.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Quit()

	home, err := conn.CurrentDir()
	if err != nil {
		home = "/"
	}
	var saved []Saved
	for _, file := range files {
		name, err := candidates(file.Name, func(n string) (bool, error) {
			_, err := conn.FileSize(path.Join(home, f.ftpPath(n)))
			return err == nil, nil
		})
		if err != nil {
			return saved, fmt.Errorf("ftp: %w", err)
		}
		full := path.Join(home, f.ftpPath(name))
		if err := mkdirAll(conn, path.Dir(full), home); err != nil {
			return saved, err
		}
		tmp := full + ".part"
		if err := conn.Stor(tmp, bytes.NewReader(file.Data)); err != nil {
			return saved, fmt.Errorf("ftp: upload %s: %w", tmp, err)
		}
		if err := conn.Rename(tmp, full); err != nil {
			_ = conn.Delete(tmp)
			return saved, fmt.Errorf("ftp: rename to %s: %w", full, err)
		}
		scheme := "ftp"
		if f.Config.TLS != "" {
			scheme = "ftps"
		}
		saved = append(saved, Saved{
			Name:     name,
			Location: fmt.Sprintf("%s://%s%s", scheme, f.Config.Host, full),
			Size:     len(file.Data),
		})
	}
	return saved, nil
}

// Open downloads a saved file; the connection closes with the reader.
func (f *FTP) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	clean, err := config.CleanRelPath(name)
	if err != nil {
		return nil, 0, err
	}
	conn, err := f.connect(ctx)
	if err != nil {
		return nil, 0, err
	}
	p := f.ftpPath(clean)
	size, err := conn.FileSize(p)
	if err != nil {
		_ = conn.Quit()
		return nil, 0, fmt.Errorf("ftp: stat %s: %w", p, err)
	}
	resp, err := conn.Retr(p)
	if err != nil {
		_ = conn.Quit()
		return nil, 0, fmt.Errorf("ftp: download %s: %w", p, err)
	}
	return &ftpReader{Response: resp, conn: conn}, size, nil
}

type ftpReader struct {
	*ftp.Response
	conn *ftp.ServerConn
}

func (r *ftpReader) Close() error {
	err := r.Response.Close()
	_ = r.conn.Quit()
	return err
}

func (f *FTP) Test(ctx context.Context) error {
	conn, err := f.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Quit()
	home, err := conn.CurrentDir()
	if err != nil {
		home = "/"
	}
	dir := path.Join(home, f.ftpPath())
	if err := mkdirAll(conn, dir, home); err != nil {
		return err
	}
	name := path.Join(dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	if err := conn.Stor(name, strings.NewReader("ok")); err != nil {
		return fmt.Errorf("ftp: write test file: %w", err)
	}
	return conn.Delete(name)
}
