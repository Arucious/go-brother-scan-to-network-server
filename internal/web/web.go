// Package web serves the status dashboard and config editor.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/app"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brother"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server is the HTTP UI.
type Server struct {
	svc        *app.Service
	configPath string
	log        *slog.Logger
	tmpl       *template.Template
}

// New creates the web server.
func New(svc *app.Service, configPath string, log *slog.Logger) *Server {
	funcs := template.FuncMap{
		"ago": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			d := time.Since(t).Round(time.Second)
			if d < time.Minute {
				return d.String() + " ago"
			}
			return d.Round(time.Minute).String() + " ago"
		},
		"clock": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"duration": func(a, b time.Time) string {
			if b.IsZero() {
				return ""
			}
			return b.Sub(a).Round(100 * time.Millisecond).String()
		},
		"functionNames": func(t config.Target) []string {
			var out []string
			for _, f := range brother.AllFunctions {
				if _, ok := t.Functions[f.Lower()]; ok {
					out = append(out, f.Lower())
				}
			}
			return out
		},
		"profile":     func(t config.Target, fn string) config.Profile { return t.Functions[fn] },
		"formats":     func() []string { return config.Formats },
		"colors":      func() []string { return config.Colors },
		"resolutions": func() []int { return config.Resolutions },
		"pageSizes":   func() []string { return config.PageSizeIDs },
		"sources":     func() []string { return config.Sources },
		"label":       optionLabel,
		"size": func(n int) string {
			switch {
			case n >= 1<<20:
				return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
			case n >= 1<<10:
				return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
			}
			return fmt.Sprintf("%d B", n)
		},
		"destination": func(d config.Destination) string {
			switch d.Type {
			case "smb":
				p := `\\` + d.Host + `\` + d.Share
				if d.Path != "" {
					p += `\` + strings.ReplaceAll(strings.Trim(d.Path, "/"), "/", `\`)
				}
				return p
			case "ftp":
				scheme := "ftp"
				if d.TLS != "" {
					scheme = "ftps"
				}
				return fmt.Sprintf("%s://%s@%s:%d/%s", scheme, d.Username, d.Host, d.Port, strings.Trim(d.Path, "/"))
			case "s3":
				where := "AWS"
				if d.Endpoint != "" {
					where = d.Endpoint
				}
				return fmt.Sprintf("s3://%s/%s (%s)", d.Bucket, strings.Trim(d.Path, "/"), where)
			}
			return d.Path
		},
	}
	tmpl := template.Must(template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html"))
	return &Server{svc: svc, configPath: configPath, log: log, tmpl: tmpl}
}

// Handler returns the HTTP handler, wrapped in basic auth when configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("POST /scan", s.scan)
	mux.HandleFunc("GET /download/{job}/{file}", s.download)
	mux.HandleFunc("POST /test", s.testDestination)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /config", s.configPage)
	mux.HandleFunc("POST /config", s.saveConfig)
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	return s.basicAuth(mux)
}

func (s *Server) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := s.svc.Config()
		if cfg == nil || cfg.Web.Username == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Web.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.Web.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="brother-scan"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type page struct {
	Title  string
	Flash  string
	Error  string
	Snap   app.Snapshot
	Config string
	Path   string
}

func (s *Server) render(w http.ResponseWriter, name string, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, p); err != nil {
		s.log.Error("render template", "template", name, "err", err)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, to, flash, errMsg string) {
	q := url.Values{}
	if flash != "" {
		q.Set("flash", flash)
	}
	if errMsg != "" {
		q.Set("error", errMsg)
	}
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dashboard.html", page{
		Title: "Status",
		Flash: r.URL.Query().Get("flash"),
		Error: r.URL.Query().Get("error"),
		Snap:  s.svc.Snapshot(),
	})
}

// optionLabel names form option values for people.
func optionLabel(v any) string {
	switch v := v.(type) {
	case int:
		return fmt.Sprintf("%d dpi", v)
	case string:
		if l, ok := map[string]string{
			"pdf": "PDF (multi-page)", "pdf-single": "PDF (one file per page)",
			"tiff": "TIFF (multi-page)", "tiff-single": "TIFF (one file per page)",
			"jpeg": "JPEG", "png": "PNG", "bmp": "BMP",
			"color": "Color", "gray": "Gray", "bw": "Black & white",
			"a4": "A4", "letter": "Letter", "legal": "Legal", "max": "Maximum area",
			"auto": "Automatic", "platen": "Flatbed glass", "adf": "Document feeder",
		}[v]; ok {
			return l
		}
		return v
	}
	return fmt.Sprint(v)
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	req := app.ScanRequest{Target: r.FormValue("target"), Function: r.FormValue("function"), Trigger: "web"}
	what := req.Target + " (" + req.Function + ")"
	if r.FormValue("custom") != "" {
		res, _ := strconv.Atoi(r.FormValue("resolution"))
		req.Function = "file"
		req.Trigger = "custom"
		req.Profile = &config.Profile{
			Format:     r.FormValue("format"),
			Color:      r.FormValue("color"),
			Resolution: res,
			PageSize:   r.FormValue("page_size"),
			Source:     r.FormValue("source"),
		}
		what = fmt.Sprintf("%s: %s, %s, %d dpi", req.Target, optionLabel(req.Profile.Format), req.Profile.Color, res)
		// Reject bad input before starting the scan in the background.
		p := config.MergeProfile("FILE", *req.Profile)
		if err := p.Validate(); err != nil {
			redirect(w, r, "/", "", "Invalid scan options: "+err.Error())
			return
		}
	}
	go func() {
		if _, err := s.svc.Scan(context.Background(), req); err != nil {
			s.log.Error("web scan failed", "target", req.Target, "function", req.Function, "err", err)
		}
	}()
	redirect(w, r, "/", "Scan started for "+what, "")
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	id, err1 := strconv.Atoi(r.PathValue("job"))
	idx, err2 := strconv.Atoi(r.PathValue("file"))
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	rc, size, file, err := s.svc.OpenJobFile(r.Context(), id, idx)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not open file: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	ctype := mime.TypeByExtension(path.Ext(file.Name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	disposition := "attachment"
	if r.URL.Query().Has("inline") {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Base()}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Warn("download interrupted", "job", id, "file", file.Name, "err", err)
	}
}

func (s *Server) testDestination(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("target")
	if err := s.svc.TestDestination(r.Context(), target); err != nil {
		redirect(w, r, "/", "", "Destination test for "+target+" failed: "+err.Error())
		return
	}
	redirect(w, r, "/", "Destination for "+target+" is writable", "")
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	s.svc.RegisterNow()
	redirect(w, r, "/", "Re-registering with the printer", "")
}

func (s *Server) configPage(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.render(w, "config.html", page{Title: "Configuration", Error: err.Error(), Path: s.configPath})
		return
	}
	if len(data) == 0 {
		data = config.Example
	}
	_, cfgErr := s.svc.Config()
	p := page{Title: "Configuration", Config: string(data), Path: s.configPath, Flash: r.URL.Query().Get("flash")}
	if cfgErr != nil {
		p.Error = cfgErr.Error()
	}
	s.render(w, "config.html", p)
}

func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	text := strings.ReplaceAll(r.FormValue("config"), "\r\n", "\n")
	cfg, err := config.Parse([]byte(text))
	if err != nil {
		s.render(w, "config.html", page{Title: "Configuration", Config: text, Path: s.configPath, Error: err.Error()})
		return
	}
	if err := writeFileAtomic(s.configPath, []byte(text)); err != nil {
		s.render(w, "config.html", page{Title: "Configuration", Config: text, Path: s.configPath, Error: "save failed: " + err.Error()})
		return
	}
	if cfg.Server.EventPort != s.currentEventPort() || cfg.Web.Listen != s.currentListen() {
		s.svc.Apply(cfg)
		redirect(w, r, "/config", "Saved. Restart the server to apply the new event port or web listen address.", "")
		return
	}
	s.svc.Apply(cfg)
	s.log.Info("configuration updated from web UI")
	redirect(w, r, "/config", "Saved and applied.", "")
}

func (s *Server) currentEventPort() int {
	if cfg, _ := s.svc.Config(); cfg != nil {
		return cfg.Server.EventPort
	}
	return brother.DefaultEventPort
}

func (s *Server) currentListen() string {
	if cfg, _ := s.svc.Config(); cfg != nil {
		return cfg.Web.Listen
	}
	return ":8080"
}

func (s *Server) apiStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.svc.Snapshot()
	out := struct {
		HostIP        string         `json:"host_ip"`
		ConfigError   string         `json:"config_error,omitempty"`
		Scanning      bool           `json:"scanning"`
		Registrations []app.RegState `json:"registrations"`
		Jobs          []app.Job      `json:"jobs"`
	}{HostIP: snap.HostIP, Scanning: snap.Scanning, Registrations: snap.Registrations, Jobs: snap.Jobs}
	if snap.ConfigError != nil {
		out.ConfigError = snap.ConfigError.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
