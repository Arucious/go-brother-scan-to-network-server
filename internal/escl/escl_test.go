package escl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestScanFeederPages(t *testing.T) {
	var next atomic.Int32
	var deleted atomic.Bool
	var settings string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			b, _ := io.ReadAll(r.Body)
			settings = string(b)
			w.Header().Set("Location", "/eSCL/ScanJobs/job1")
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/eSCL/ScanJobs/job1/NextDocument":
			switch next.Add(1) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
			case 2, 3:
				_, _ = w.Write([]byte("page"))
			default:
				http.NotFound(w, r)
			}
		case r.Method == http.MethodDelete:
			deleted.Store(true)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	feederLoadDelay = 0
	c := New(strings.TrimPrefix(srv.URL, "http://"))
	var got int
	n, err := c.Scan(context.Background(), Settings{Source: SourceFeeder, ColorMode: ColorGray, Resolution: 300, Width: 2480, Height: 3507, Format: "image/jpeg"},
		func(page []byte) error { got++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || got != 2 {
		t.Fatalf("pages = %d, callbacks = %d", n, got)
	}
	if deleted.Load() {
		t.Fatal("finished job should not be deleted")
	}
	if !strings.Contains(settings, "<scan:ColorMode>Grayscale8</scan:ColorMode>") {
		t.Errorf("settings missing color mode:\n%s", settings)
	}
}

// TestSettingsElementOrder pins the request layout verified against a
// Brother MFC-L2800DW; other orders are rejected or ignored by the device.
func TestSettingsElementOrder(t *testing.T) {
	got := string(Settings{Source: SourcePlaten, ColorMode: ColorGray, Resolution: 100, Width: 2480, Height: 3507, Format: "image/jpeg"}.XML())
	order := []string{
		"<pwg:Version>2.0</pwg:Version>",
		"<scan:Intent>Document</scan:Intent>",
		"<pwg:ContentRegionUnits>",
		"<pwg:XOffset>0</pwg:XOffset>",
		"<pwg:YOffset>0</pwg:YOffset>",
		"<pwg:Width>2480</pwg:Width>",
		"<pwg:Height>3507</pwg:Height>",
		"<pwg:InputSource>Platen</pwg:InputSource>",
		"<scan:ColorMode>Grayscale8</scan:ColorMode>",
		"<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>",
		"<scan:DocumentFormatExt>image/jpeg</scan:DocumentFormatExt>",
		"<scan:XResolution>100</scan:XResolution>",
		"<scan:YResolution>100</scan:YResolution>",
	}
	pos := 0
	for _, want := range order {
		i := strings.Index(got[pos:], want)
		if i < 0 {
			t.Fatalf("%s missing or out of order in:\n%s", want, got)
		}
		pos += i + len(want)
	}
	if strings.Contains(got, "\n") {
		t.Fatal("settings must be compact")
	}
}

func TestScanNoPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", absoluteLocation(r))
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"))
	if _, err := c.Scan(context.Background(), Settings{Source: SourcePlaten}, func([]byte) error { return nil }); err == nil {
		t.Fatal("expected error for empty job")
	}
}

// absoluteLocation returns an absolute Location, as some devices send.
func absoluteLocation(r *http.Request) string {
	return "http://" + r.Host + "/eSCL/ScanJobs/abc/"
}

func TestCapabilitiesParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<scan:ScannerCapabilities xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
<pwg:Version>2.93</pwg:Version><pwg:MakeAndModel>Brother MFC-L2800DW</pwg:MakeAndModel>
<scan:Platen><scan:PlatenInputCaps><scan:MaxWidth>2550</scan:MaxWidth><scan:MaxHeight>3507</scan:MaxHeight></scan:PlatenInputCaps></scan:Platen>
<scan:Adf><scan:AdfSimplexInputCaps><scan:MaxWidth>2550</scan:MaxWidth><scan:MaxHeight>4200</scan:MaxHeight></scan:AdfSimplexInputCaps></scan:Adf>
</scan:ScannerCapabilities>`))
	}))
	defer srv.Close()
	caps, err := New(strings.TrimPrefix(srv.URL, "http://")).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Version != "2.93" || caps.MakeAndModel != "Brother MFC-L2800DW" {
		t.Fatalf("unexpected caps %+v", caps)
	}
	if w, h := caps.MaxSize(SourceFeeder); w != 2550 || h != 4200 {
		t.Fatalf("feeder size %dx%d", w, h)
	}
	if w, h := caps.MaxSize(SourcePlaten); w != 2550 || h != 3507 {
		t.Fatalf("platen size %dx%d", w, h)
	}
}
