package destination

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// SMB stores scans on an SMB2/3 share.
type SMB struct {
	Config config.Destination
}

// smbPath joins and converts a '/' path to the share's '\' form.
func smbPath(parts ...string) string {
	p := strings.Trim(path.Join(parts...), "/")
	return strings.ReplaceAll(p, "/", `\`)
}

func (s *SMB) mount(ctx context.Context) (*smb2.Share, func(), error) {
	c := s.Config
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("smb: connect %s: %w", addr, err)
	}
	dialer := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{
		User:     c.Username,
		Password: c.Password,
		Domain:   c.Domain,
	}}
	session, err := dialer.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("smb: login to %s as %q: %w", addr, c.Username, err)
	}
	share, err := session.Mount(c.Share)
	if err != nil {
		_ = session.Logoff()
		conn.Close()
		return nil, nil, fmt.Errorf("smb: mount share %q: %w", c.Share, err)
	}
	share = share.WithContext(ctx)
	closeFn := func() {
		_ = share.Umount()
		_ = session.Logoff()
		conn.Close()
	}
	return share, closeFn, nil
}

func (s *SMB) Save(ctx context.Context, files []File) ([]Saved, error) {
	share, closeFn, err := s.mount(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	var saved []Saved
	for _, f := range files {
		name, err := candidates(f.Name, func(n string) (bool, error) {
			_, err := share.Stat(smbPath(s.Config.Path, n))
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return saved, fmt.Errorf("smb: %w", err)
		}
		full := smbPath(s.Config.Path, name)
		if dir := smbPath(path.Dir(path.Join(s.Config.Path, name))); dir != "" && dir != "." {
			if err := share.MkdirAll(dir, 0o755); err != nil {
				return saved, fmt.Errorf("smb: create directory %s: %w", dir, err)
			}
		}
		tmp := full + ".part"
		if err := writeSMBFile(share, tmp, f.Data); err != nil {
			_ = share.Remove(tmp)
			return saved, fmt.Errorf("smb: write %s: %w", tmp, err)
		}
		if err := share.Rename(tmp, full); err != nil {
			_ = share.Remove(tmp)
			return saved, fmt.Errorf("smb: rename to %s: %w", full, err)
		}
		saved = append(saved, Saved{
			Name:     name,
			Location: fmt.Sprintf(`\\%s\%s\%s`, s.Config.Host, s.Config.Share, full),
			Size:     len(f.Data),
		})
	}
	return saved, nil
}

// Open reads a saved file; the SMB session stays open until the reader is closed.
func (s *SMB) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	clean, err := config.CleanRelPath(name)
	if err != nil {
		return nil, 0, err
	}
	share, closeFn, err := s.mount(ctx)
	if err != nil {
		return nil, 0, err
	}
	f, err := share.Open(smbPath(s.Config.Path, clean))
	if err != nil {
		closeFn()
		return nil, 0, fmt.Errorf("smb: open %s: %w", clean, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		closeFn()
		return nil, 0, fmt.Errorf("smb: stat %s: %w", clean, err)
	}
	return &smbReader{File: f, closeFn: closeFn}, st.Size(), nil
}

type smbReader struct {
	*smb2.File
	closeFn func()
}

func (r *smbReader) Close() error {
	err := r.File.Close()
	r.closeFn()
	return err
}

func writeSMBFile(share *smb2.Share, name string, data []byte) error {
	f, err := share.Create(name)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *SMB) Test(ctx context.Context) error {
	share, closeFn, err := s.mount(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	if dir := smbPath(s.Config.Path); dir != "" {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("smb: create directory %s: %w", dir, err)
		}
	}
	name := smbPath(s.Config.Path, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	if err := writeSMBFile(share, name, []byte("ok")); err != nil {
		return fmt.Errorf("smb: write test file: %w", err)
	}
	return share.Remove(name)
}
