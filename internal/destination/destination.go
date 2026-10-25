// Package destination stores finished scans.
package destination

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// File is one output file.
type File struct {
	// Name is the path relative to the destination root, using '/'.
	Name string
	Data []byte
}

// Saved describes a stored file.
type Saved struct {
	// Name is the final path relative to the destination root, using '/'.
	Name string
	// Location is a human-readable full location (local path or UNC path).
	Location string
	Size     int
}

// Destination stores files.
type Destination interface {
	// Save writes all files. Existing files are never overwritten; a numeric
	// suffix is added instead.
	Save(ctx context.Context, files []File) ([]Saved, error)
	// Open reads a previously saved file by its relative name.
	Open(ctx context.Context, name string) (io.ReadCloser, int64, error)
	// Test checks that the destination is reachable and writable.
	Test(ctx context.Context) error
}

// New builds a destination from config.
func New(c config.Destination) (Destination, error) {
	switch c.Type {
	case "local":
		return &Local{Root: c.Path, ArchivePath: c.ArchivePath}, nil
	case "smb":
		return &SMB{Config: c}, nil
	case "ftp":
		return &FTP{Config: c}, nil
	case "s3":
		return &S3{Config: c}, nil
	default:
		return nil, fmt.Errorf("unknown destination type %q", c.Type)
	}
}

// candidates yields name, name-1, name-2, ... preserving the extension.
func candidates(name string, exists func(string) (bool, error)) (string, error) {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		ok, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free file name for %s", name)
}
