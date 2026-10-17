package destination

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalSaveNoOverwrite(t *testing.T) {
	root := t.TempDir()
	l := &Local{Root: root}
	if err := l.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := l.Save(context.Background(), []File{{Name: "2026/scan.pdf", Data: []byte{byte(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]byte{"2026/scan.pdf": 0, "2026/scan-1.pdf": 1} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || len(b) != 1 || b[0] != want {
			t.Fatalf("%s: %v %v", name, b, err)
		}
		rc, size, err := l.Open(context.Background(), name)
		if err != nil || size != 1 {
			t.Fatalf("open %s: %v size=%d", name, err, size)
		}
		rc.Close()
	}
	if _, _, err := l.Open(context.Background(), "../etc/passwd"); err == nil {
		t.Fatal("path escape allowed")
	}
}
