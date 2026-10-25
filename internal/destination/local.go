package destination

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// Local stores scans in a directory (e.g. a mounted volume).
type Local struct {
	Root        string
	ArchivePath string
}

func (l *Local) Save(_ context.Context, files []File) ([]Saved, error) {
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(l.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var saved []Saved
	for _, f := range files {
		clean, err := config.CleanRelPath(f.Name)
		if err != nil {
			return saved, err
		}
		name, err := candidates(clean, func(n string) (bool, error) {
			_, err := root.Lstat(filepath.FromSlash(n))
			if errors.Is(err, fs.ErrNotExist) && l.ArchivePath != "" {
				_, err = root.Lstat(filepath.Join(filepath.FromSlash(l.ArchivePath), filepath.FromSlash(n)))
			}
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return saved, err
		}
		full := filepath.Join(l.Root, filepath.FromSlash(name))
		rel := filepath.FromSlash(name)
		if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
			return saved, err
		}
		tmp := filepath.Join(filepath.Dir(rel), ".scan-"+rand.Text()+".part")
		out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return saved, err
		}
		_, writeErr := out.Write(f.Data)
		closeErr := out.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = root.Remove(tmp)
			return saved, err
		}
		if err := root.Rename(tmp, rel); err != nil {
			_ = root.Remove(tmp)
			return saved, err
		}
		saved = append(saved, Saved{Name: name, Location: full, Size: len(f.Data)})
	}
	return saved, nil
}

func (l *Local) Open(_ context.Context, name string) (io.ReadCloser, int64, error) {
	clean, err := config.CleanRelPath(name)
	if err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(l.Root)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(clean))
	if errors.Is(err, fs.ErrNotExist) && l.ArchivePath != "" {
		f, err = root.Open(filepath.Join(filepath.FromSlash(l.ArchivePath), filepath.FromSlash(clean)))
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (l *Local) Test(_ context.Context) error {
	if err := os.MkdirAll(l.Root, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(l.Root, ".write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
