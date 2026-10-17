package config

import (
	"strings"
	"testing"
	"time"
)

func TestExampleParses(t *testing.T) {
	t.Setenv("SMB_PASSWORD", "secret")
	cfg, err := Parse(Example)
	if err != nil {
		t.Fatal(err)
	}
	nas, ok := cfg.Target("nas")
	if !ok {
		t.Fatal("target NAS missing")
	}
	if nas.Destination.Password != "secret" || nas.Destination.Port != 445 {
		t.Fatalf("unexpected destination %+v", nas.Destination)
	}
	if p := nas.Functions["image"]; p.Format != "jpeg" || p.Color != "color" || p.Resolution != 300 || p.Source != "auto" || p.PageSize != "a4" {
		t.Fatalf("image defaults not applied: %+v", p)
	}
	if _, ok := nas.Functions["ocr"]; ok {
		t.Fatal("unlisted function registered")
	}
	if cfg.Printer.PhoenixUser != "Public" || cfg.Printer.PhoenixPassword != "0000" || cfg.Printer.ScanProtocol != "native" {
		t.Fatalf("phoenix defaults not applied: %+v", cfg.Printer)
	}
	types := map[string]string{}
	for _, tg := range cfg.Targets {
		types[tg.Destination.Type] = tg.Name
	}
	for _, typ := range DestinationTypes {
		if types[typ] == "" {
			t.Errorf("example has no %s destination", typ)
		}
	}
	if ftp, _ := cfg.Target("FTP"); ftp.Destination.Port != 21 {
		t.Errorf("ftp port default = %d", ftp.Destination.Port)
	}
	if local, _ := cfg.Target("Local"); local.Functions["file"].Color != "bw" || local.Functions["file"].Format != "pdf-single" {
		t.Errorf("local profile = %+v", local.Functions["file"])
	}
}

func TestDestinationDefaultsAndValidation(t *testing.T) {
	cfg, err := Parse([]byte(`
printer: {address: 10.0.0.2}
targets:
  - {name: A, destination: {type: smb, host: nas, share: s}}
  - {name: B, destination: {type: ftp, host: ftp, tls: implicit}}
  - {name: C, destination: {type: s3, bucket: b}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.Targets[0].Destination; d.Username != "guest" || d.Port != 445 {
		t.Errorf("smb defaults %+v", d)
	}
	if d := cfg.Targets[1].Destination; d.Username != "anonymous" || d.Port != 990 {
		t.Errorf("ftp defaults %+v", d)
	}

	_, err = Parse([]byte(`
printer: {address: 10.0.0.2}
targets:
  - {name: A, destination: {type: ftp, tls: always}}
  - {name: B, destination: {type: s3, access_key_id: x}}
  - {name: C, destination: {type: webdav}}
`))
	for _, want := range []string{"host is required for ftp", "tls must be", "bucket is required", "set together", "type must be one of local, smb, ftp, s3"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestAllFunctionsByDefault(t *testing.T) {
	cfg, err := Parse([]byte(`
printer: {address: 10.0.0.2}
targets:
  - name: Office
    destination: {type: local, path: /scans}
`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cfg.Targets[0].Functions); n != 4 {
		t.Fatalf("expected 4 functions, got %d", n)
	}
}

func TestValidation(t *testing.T) {
	_, err := Parse([]byte(`
printer: {address: ""}
targets:
  - name: ThisNameIsWayTooLong
    functions: {fax: {}, file: {resolution: 150, format: xps}}
    destination: {type: webdav}
`))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"printer.address", "15 characters", "unknown scan function", "resolution", "format", "destination.type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestUnknownField(t *testing.T) {
	if _, err := Parse([]byte("printer: {adress: x}\n")); err == nil { //nolint:misspell // deliberate unknown field
		t.Fatal("unknown field accepted")
	}
}

func TestRenderFilename(t *testing.T) {
	tg := Target{Name: "NAS", Filename: `{{.Time.Format "2006/01"}}/{{.Target}}: {{.Function}}`}
	got, err := tg.RenderFilename(FilenameData{Time: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), Target: "NAS", Function: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026/10/NAS_ file" {
		t.Fatalf("got %q", got)
	}
	if _, err := CleanRelPath("../etc/passwd"); err == nil {
		t.Fatal("path escape accepted")
	}
}
