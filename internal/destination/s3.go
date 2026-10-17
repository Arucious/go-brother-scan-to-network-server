package destination

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

// S3 stores scans in an S3 bucket (AWS or any S3-compatible service).
type S3 struct {
	Config config.Destination
}

func (s *S3) client() (*minio.Client, error) {
	c := s.Config
	endpoint, secure := c.Endpoint, true
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	} else if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		endpoint, secure = u.Host, u.Scheme != "http"
	}
	opts := &minio.Options{Secure: secure, Region: c.Region}
	if c.AccessKeyID != "" {
		opts.Creds = credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, c.SessionToken)
	} else {
		opts.Creds = credentials.NewStatic("", "", "", credentials.SignatureAnonymous)
	}
	if c.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	if c.InsecureSkipVerify {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in
		opts.Transport = tr
	}
	cl, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	return cl, nil
}

func (s *S3) key(name string) string {
	return strings.TrimPrefix(path.Join(s.Config.Path, name), "/")
}

func (s *S3) exists(ctx context.Context, cl *minio.Client, key string) (bool, error) {
	_, err := cl.StatObject(ctx, s.Config.Bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	switch minio.ToErrorResponse(err).Code {
	case minio.NoSuchKey, "NotFound":
		return false, nil
	}
	return false, fmt.Errorf("s3: check %s: %w", key, err)
}

func (s *S3) Save(ctx context.Context, files []File) ([]Saved, error) {
	cl, err := s.client()
	if err != nil {
		return nil, err
	}
	var saved []Saved
	for _, f := range files {
		name, err := candidates(f.Name, func(n string) (bool, error) { return s.exists(ctx, cl, s.key(n)) })
		if err != nil {
			return saved, err
		}
		key := s.key(name)
		opts := minio.PutObjectOptions{
			ContentType:  mime.TypeByExtension(path.Ext(name)),
			StorageClass: s.Config.StorageClass,
		}
		if _, err := cl.PutObject(ctx, s.Config.Bucket, key, bytes.NewReader(f.Data), int64(len(f.Data)), opts); err != nil {
			return saved, fmt.Errorf("s3: upload %s: %w", key, err)
		}
		saved = append(saved, Saved{
			Name:     name,
			Location: fmt.Sprintf("s3://%s/%s", s.Config.Bucket, key),
			Size:     len(f.Data),
		})
	}
	return saved, nil
}

func (s *S3) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	clean, err := config.CleanRelPath(name)
	if err != nil {
		return nil, 0, err
	}
	cl, err := s.client()
	if err != nil {
		return nil, 0, err
	}
	key := s.key(clean)
	obj, err := cl.GetObject(ctx, s.Config.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("s3: download %s: %w", key, err)
	}
	st, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, fmt.Errorf("s3: download %s: %w", key, err)
	}
	return obj, st.Size, nil
}

func (s *S3) Test(ctx context.Context) error {
	cl, err := s.client()
	if err != nil {
		return err
	}
	key := s.key(fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	if _, err := cl.PutObject(ctx, s.Config.Bucket, key, strings.NewReader("ok"), 2, minio.PutObjectOptions{}); err != nil {
		return fmt.Errorf("s3: write test object to bucket %q: %w", s.Config.Bucket, err)
	}
	return cl.RemoveObject(ctx, s.Config.Bucket, key, minio.RemoveObjectOptions{})
}
