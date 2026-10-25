package destination

import (
	"context"
	"io"
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

func TestLocalDownloadAfterUploadAndAvoidArchivedName(t *testing.T) {
	root := t.TempDir()
	l := &Local{Root: root, ArchivePath: "uploaded"}
	if err := os.MkdirAll(filepath.Join(root, "uploaded", "2026"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uploaded", "2026", "scan.pdf"), []byte("archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, _, err := l.Open(context.Background(), "2026/scan.pdf")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(data) != "archived" {
		t.Fatalf("download: %q %v", data, err)
	}
	saved, err := l.Save(context.Background(), []File{{Name: "2026/scan.pdf", Data: []byte("new")}})
	if err != nil {
		t.Fatal(err)
	}
	if saved[0].Name != "2026/scan-1.pdf" {
		t.Fatalf("reused archived name: %s", saved[0].Name)
	}
}

func TestLocalRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.pdf"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l := &Local{Root: root}
	if rc, _, err := l.Open(context.Background(), "escape/secret.pdf"); err == nil {
		rc.Close()
		t.Fatal("read escaped destination root")
	}
	if _, err := l.Save(context.Background(), []File{{Name: "escape/new.pdf", Data: []byte("scan")}}); err == nil {
		t.Fatal("write escaped destination root")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.pdf")); !os.IsNotExist(err) {
		t.Fatalf("unexpected outside file: %v", err)
	}
	if _, err := l.Save(context.Background(), []File{{Name: "../new.pdf", Data: []byte("scan")}}); err == nil {
		t.Fatal("parent path accepted")
	}
	l.ArchivePath = "escape"
	if rc, _, err := l.Open(context.Background(), "secret.pdf"); err == nil {
		rc.Close()
		t.Fatal("archive fallback escaped destination root")
	}
}
