package brother

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistrationValue(t *testing.T) {
	got := Registration{HostIP: "10.0.0.5", User: "NAS", Function: FuncFile}.Value()
	want := `TYPE=BR;BUTTON=SCAN;DURATION=360;CC=1;HOST=10.0.0.5:54925;USER="NAS";FUNC=FILE;APPNUM=5;`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	got = Registration{HostIP: "10.0.0.5", Port: 5000, User: "A", Function: FuncImage, PIN: "1234"}.Value()
	want = `TYPE=BR;BUTTON=SCAN;DURATION=360;CC=1;HOST=10.0.0.5:5000;USER="A";BRID=1234;FUNC=IMAGE;APPNUM=1;`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestParseEventPacket(t *testing.T) {
	payload := `TYPE=BR;BUTTON=SCAN;USER="NAS";FUNC=OCR;HOST=10.0.0.5:54925;APPNUM=3;`
	packet := append([]byte{0x02, 0x00, byte(len(payload)), 0x30}, payload...)
	packet = append(packet, 0)

	ev, ok, err := ParseEventPacket(packet)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ev.User != "NAS" || ev.Function != FuncOCR {
		t.Fatalf("unexpected event %+v", ev)
	}
	if v, _ := ev.Info.Get("APPNUM"); v != "3" {
		t.Fatalf("APPNUM = %q", v)
	}

	if _, ok, _ := ParseEventPacket([]byte{0x01, 0, 0, 0x01, 'A', '=', 'B', ';'}); ok {
		t.Fatal("non-event packet accepted")
	}
	if _, ok, err := ParseEventPacket([]byte{0x02}); ok || err == nil {
		t.Fatal("short packet accepted")
	}
}

func TestParseFunction(t *testing.T) {
	for _, in := range []string{"file", "FILE", " Image "} {
		if _, err := ParseFunction(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := ParseFunction("fax"); err == nil {
		t.Error("fax accepted")
	}
}

func TestDigestAuthorizationRFC2617(t *testing.T) {
	// RFC 2617 section 3.5 example, with a fixed cnonce substituted below.
	c := digestChallenge{realm: "testrealm@host.com", nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093", opaque: "5ccc069c403ebaf9f0171e9517f40e41"}
	h := c.authorization("GET", "/dir/index.html", "Mufasa", "Circle Of Life")
	if !strings.Contains(h, `response="670fd8c2df070c60b045671b8b24ff02"`) {
		t.Fatalf("unexpected digest header without qop: %s", h)
	}
	if !strings.Contains(h, `opaque="5ccc069c403ebaf9f0171e9517f40e41"`) {
		t.Fatalf("opaque missing: %s", h)
	}
}

func TestParseDigestChallenge(t *testing.T) {
	c, err := parseDigestChallenge(`Digest realm="Machine", nonce="73209F9DA7E9717F", opaque="f097", algorithm="MD5", qop="auth"`)
	if err != nil {
		t.Fatal(err)
	}
	if c.realm != "Machine" || c.nonce != "73209F9DA7E9717F" || c.opaque != "f097" || c.qop != "auth" {
		t.Fatalf("unexpected challenge %+v", c)
	}
	if _, err := parseDigestChallenge(`Basic realm="x"`); err == nil {
		t.Fatal("basic accepted")
	}
}

func TestPhoenixRegister(t *testing.T) {
	var gotBody phoenixRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/phoenix/mib" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="Machine", nonce="abc", opaque="def", algorithm="MD5", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		auth := r.Header.Get("Authorization")
		for _, want := range []string{`username="Public"`, `realm="Machine"`, `nonce="abc"`, `uri="/phoenix/mib"`, `qop=auth`, `opaque="def"`} {
			if !strings.Contains(auth, want) {
				t.Errorf("authorization missing %s: %s", want, auth)
			}
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("bad body %s: %v", body, err)
		}
		_, _ = w.Write([]byte(`{"response":[{"key":"` + ScanKeyOID + `","status":0}]}`))
	}))
	defer srv.Close()

	p := NewPhoenix(strings.TrimPrefix(srv.URL, "https://"), "Public", "0000")
	if err := p.Register(context.Background(), "TYPE=BR;"); err != nil {
		t.Fatal(err)
	}
	if len(gotBody.Request) != 1 || gotBody.Request[0].Key != ScanKeyOID || *gotBody.Request[0].StringValue != "TYPE=BR;" {
		t.Fatalf("unexpected request %+v", gotBody)
	}
}

func TestPhoenixRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response":[{"key":"` + ScanKeyOID + `","status":2}]}`))
	}))
	defer srv.Close()
	p := NewPhoenix(strings.TrimPrefix(srv.URL, "https://"), "Public", "0000")
	if err := p.Register(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "status 2") {
		t.Fatalf("expected rejection, got %v", err)
	}
}
