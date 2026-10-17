package destination

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// Local stores scans in a directory (e.g. a mounted volume).
type Local struct {
	Root string
}

func (l *Local) Save(_ context.Context, files []File) ([]Saved, error) {
	var saved []Saved
	for _, f := range files {
		name, err := candidates(f.Name, func(n string) (bool, error) {
			_, err := os.Stat(filepath.Join(l.Root, filepath.FromSlash(n)))
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return saved, err
		}
		full := filepath.Join(l.Root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return saved, err
		}
		tmp := full + ".part"
		if err := os.WriteFile(tmp, f.Data, 0o644); err != nil {
			return saved, err
		}
		if err := os.Rename(tmp, full); err != nil {
			os.Remove(tmp)
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
	f, err := os.Open(filepath.Join(l.Root, filepath.FromSlash(clean)))
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
