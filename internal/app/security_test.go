package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

func TestEventSourceFilter(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(log, &config.Config{Printer: config.Printer{Address: "192.0.2.15"}}, nil)
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"192.0.2.15", true}, {"::ffff:192.0.2.15", true}, {"192.0.2.16", false}, {"127.0.0.1", false},
	} {
		if got := s.acceptEvent(&net.UDPAddr{IP: net.ParseIP(tc.ip)}); got != tc.want {
			t.Errorf("source %s: got %v, want %v", tc.ip, got, tc.want)
		}
	}
	if s.acceptEvent(nil) {
		t.Fatal("nil sender accepted")
	}
	s.setConfig(nil, nil)
	if s.acceptEvent(&net.UDPAddr{IP: net.ParseIP("192.0.2.15")}) {
		t.Fatal("sender accepted after invalidating config")
	}
}

func TestBusyScanDoesNotQueueOrCreateJob(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.Parse([]byte("printer: {address: 192.0.2.15}\ntargets:\n  - name: test\n    destination: {type: local, path: /scans}\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(log, cfg, nil)
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Scan(ctx, ScanRequest{Target: "test", Function: "file"}); done <- err }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "scanner is busy" {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan queued instead of rejecting a busy scanner")
	}
	if len(s.Snapshot().Jobs) != 0 {
		t.Fatal("busy scan created a job")
	}
}
