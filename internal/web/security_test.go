package web

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/app"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

func TestRequestSecurity(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Web: config.Web{Username: "scanner", Password: "test-password"}}
	svc := app.New(log, cfg, nil)
	h := New(svc, t.TempDir()+"/config.yaml", log).Handler()
	for _, tc := range []struct {
		name, method, path, origin, fetch string
		auth                              bool
		want                              int
	}{
		{"anonymous config", "GET", "/config", "", "", false, 401},
		{"anonymous health", "GET", "/healthz", "", "", false, 200},
		{"authenticated status", "GET", "/api/status", "", "", true, 200},
		{"cross-origin form", "POST", "/register", "https://attacker.example", "", true, 403},
		{"cross-site fetch", "POST", "/register", "", "cross-site", true, 403},
		{"same-origin form", "POST", "/register", "http://scanner.example", "same-origin", true, 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://scanner.example"+tc.path, nil)
			if tc.auth {
				r.SetBasicAuth("scanner", "test-password")
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetch)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestInvalidConfigFailsClosed(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(app.New(log, nil, errors.New("invalid")), t.TempDir()+"/config.yaml", log).Handler()
	for _, route := range []string{"/", "/config", "/api/status"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://scanner.example"+route, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d", route, w.Code)
		}
	}
	body := url.Values{"config": {"printer: {address: 192.0.2.1}"}}.Encode()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://scanner.example/config", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("save status = %d", w.Code)
	}
}
