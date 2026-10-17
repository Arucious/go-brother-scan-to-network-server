package destination

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// Integration tests against real servers, enabled with DEST_IT=1. Start them
// with `make integration-servers` (Samba on :1445, FTP on :2121, S3 on :9000).
func integration(t *testing.T) {
	t.Helper()
	if os.Getenv("DEST_IT") == "" {
		t.Skip("set DEST_IT=1 and start the test servers (make integration-servers)")
	}
}

func exercise(t *testing.T, d Destination) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if err := d.Test(ctx); err != nil {
		t.Fatalf("Test: %v", err)
	}
	dir := "it-" + time.Now().Format("150405.000")
	data := []byte("scan " + dir)
	var names []string
	for range 2 {
		saved, err := d.Save(ctx, []File{{Name: dir + "/sub/scan.pdf", Data: data}})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		if len(saved) != 1 || saved[0].Size != len(data) || saved[0].Location == "" {
			t.Fatalf("unexpected saved %+v", saved)
		}
		names = append(names, saved[0].Name)
	}
	if names[0] != dir+"/sub/scan.pdf" || names[1] != dir+"/sub/scan-1.pdf" {
		t.Fatalf("names = %v", names)
	}
	for _, n := range names {
		rc, size, err := d.Open(ctx, n)
		if err != nil {
			t.Fatalf("Open %s: %v", n, err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || size != int64(len(data)) || !bytes.Equal(got, data) {
			t.Fatalf("Open %s: size=%d got=%q err=%v", n, size, got, err)
		}
	}
}

func TestIntegrationSMB(t *testing.T) {
	integration(t)
	exercise(t, &SMB{Config: config.Destination{Type: "smb", Host: "127.0.0.1", Port: 1445, Share: "scans", Path: "inbox", Username: "scan", Password: "secret"}})
}

func TestIntegrationFTP(t *testing.T) {
	integration(t)
	exercise(t, &FTP{Config: config.Destination{Type: "ftp", Host: "127.0.0.1", Port: 2121, Path: "inbox", Username: "scan", Password: "secret"}})
}

func TestIntegrationS3(t *testing.T) {
	integration(t)
	d := &S3{Config: config.Destination{Type: "s3", Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "scans",
		Path: "brother", AccessKeyID: "minioadmin", SecretAccessKey: "minioadmin", PathStyle: true}}
	cl, err := d.client()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := cl.BucketExists(ctx, "scans"); err != nil {
		t.Fatal(err)
	} else if !ok {
		if err := cl.MakeBucket(ctx, "scans", minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	exercise(t, d)
}
