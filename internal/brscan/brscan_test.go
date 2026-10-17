package brscan

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// device scripts the device side of one connection.
type device struct {
	gkp    []byte // reply to GKP; nil = do not answer (v1 device)
	k      []byte // reply to K
	stream func(conn net.Conn)
	gotX   chan string
}

func (d *device) serve(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	d.gotX = make(chan string, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.handle(conn)
		}
	}()
	return ln.Addr().String()
}

func (d *device) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	readMsg := func() string { b, _ := r.ReadBytes(msgEnd); return string(b) }
	_, _ = conn.Write([]byte("+OK 200\r\n"))
	switch msg := readMsg(); {
	case strings.HasPrefix(msg, "\x1bGKP"):
		if d.gkp == nil {
			_, _ = io.Copy(io.Discard, r) // v1 device ignores GKP
			return
		}
		_, _ = conn.Write(d.gkp)
	case strings.HasPrefix(msg, "\x1bK"):
		_, _ = conn.Write(d.k)
	}
	if !strings.HasPrefix(readMsg(), "\x1bI") {
		return
	}
	dims := "300,300,2,209,2480,291,3437,"
	_, _ = conn.Write(append([]byte{0, byte(len(dims) + 1), 0}, append([]byte(dims), 0)...))
	d.gotX <- readMsg()
	d.stream(conn)
	_, _ = conn.Write([]byte{msgEnd})
	_, _ = io.Copy(io.Discard, r)
}

func gkpReply(result byte, body []byte) []byte {
	b := append([]byte{result, 'G', 'K', 'P'}, binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	return append(b, body...)
}

func tlv(tag uint16, v ...byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, tag)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(v)))
	return append(b, v...)
}

// classicStream sends JPEG pages in the host-settings framing.
func classicStream(pages ...[]byte) func(net.Conn) {
	return func(conn net.Conn) {
		for i, p := range pages {
			pg := byte(i + 1)
			for len(p) > 0 {
				n := min(len(p), 1000)
				_, _ = conn.Write([]byte{blockJPEG, blockMagic, 0, pg, 0, 0, 0, 0, 0, 0, byte(n), byte(n >> 8)})
				_, _ = conn.Write(p[:n])
				p = p[n:]
			}
			_, _ = conn.Write([]byte{blockPageEnd, blockMagic, 0, pg, 0, 0x84, 0, 0, 0, 0})
		}
	}
}

func extHeader(kind byte, pg byte, mode uint16, n int) []byte {
	h := []byte{0x00, kind, pg, 0x00}
	h = binary.LittleEndian.AppendUint16(h, mode)
	h = binary.LittleEndian.AppendUint32(h, uint32(n))
	return binary.LittleEndian.AppendUint32(h, 0x60)
}

func collect(t *testing.T, addr string, s Settings) ([]page.Page, Result) {
	t.Helper()
	c := New(addr)
	c.PageTimeout = 5 * time.Second
	var pages []page.Page
	res, err := c.Scan(context.Background(), s, func(p page.Page) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pages, res
}

func TestScanIdle(t *testing.T) {
	img := testJPEG(t, 16, 16)
	d := &device{gkp: gkpReply(0x81, nil), stream: classicStream(img, img)}
	pages, res := collect(t, d.serve(t), Settings{Resolution: 300, Mode: ModeGray, Width: 2000})
	if len(pages) != 2 || res.Pages != 2 || res.Panel != nil || res.DeviceSettings {
		t.Fatalf("pages=%d res=%+v", len(pages), res)
	}
	if !bytes.Equal(pages[0].Data, img) || pages[0].Kind != page.JPEG || pages[0].XDPI != 300 {
		t.Fatalf("unexpected page %+v", pages[0])
	}
	x := <-d.gotX
	for _, want := range []string{"\x1bX\n", "A=0,0,2000,3437\n", "C=JPEG\n", "M=GRAY64\n", "R=300,300\n"} {
		if !strings.Contains(x, want) {
			t.Errorf("X message missing %q: %q", want, x)
		}
	}
}

// TestScanPanelJPEG covers a button scan with panel settings: GKP reports
// TIFF multi-page and the data uses the extended framing.
func TestScanPanelJPEG(t *testing.T) {
	a, b := testJPEG(t, 8, 8), testJPEG(t, 8, 16)
	body := append(tlv(tagScanKey, 0x02, 0x01), tlv(tagScan, 0xc8, 0, 0xc8, 0, 0x01, 0x12, 0, 50, 50, 0)...)
	body = append(body, tlv(tagFileSave, 0x12, 0x00, 0x00, 0x00)...)
	body = append(body, tlv(tagEnd)...)
	d := &device{gkp: gkpReply(0, body), stream: func(conn net.Conn) {
		for i, p := range [][]byte{a, b} {
			pg := byte(i + 1)
			for len(p) > 0 {
				n := min(len(p), 100)
				_, _ = conn.Write(extHeader(extData, pg, 0x15, n))
				_, _ = conn.Write(p[:n])
				p = p[n:]
			}
			_, _ = conn.Write([]byte{0x00, extPageEnd, pg, 0x00})
		}
	}}
	pages, res := collect(t, d.serve(t), Settings{Resolution: 300})
	if len(pages) != 2 || !bytes.Equal(pages[0].Data, a) || !bytes.Equal(pages[1].Data, b) {
		t.Fatalf("got %d pages", len(pages))
	}
	if !res.DeviceSettings || res.ModeCode != 0x15 || res.Panel == nil {
		t.Fatalf("unexpected result %+v", res)
	}
	if p := res.Panel; p.FileType() != "TIFF Multi-Page" || p.XRes != 200 || p.ColorType != 1 {
		t.Fatalf("unexpected panel %+v", p)
	}
}

func TestScanPanelBilevel(t *testing.T) {
	rows := [][]byte{bytes.Repeat([]byte{0}, 4), {0xff, 0x00, 0x0f, 0xf0}, bytes.Repeat([]byte{0x80}, 4)}
	body := append(tlv(tagScan, 0x2c, 0x01, 0x2c, 0x01, 0x00, 0x12, 0, 50, 50, 0), tlv(tagEnd)...)
	d := &device{gkp: gkpReply(0, body), stream: func(conn net.Conn) {
		for _, row := range rows {
			enc := page.PackBits(nil, row)
			_, _ = conn.Write(extHeader(extBilevel, 1, 0x08, len(enc)))
			_, _ = conn.Write(enc)
		}
		_, _ = conn.Write([]byte{0x00, extPageEnd, 1, 0x00})
	}}
	pages, _ := collect(t, d.serve(t), Settings{Resolution: 200})
	if len(pages) != 1 {
		t.Fatalf("got %d pages", len(pages))
	}
	p := pages[0]
	if p.Kind != page.Bilevel || p.Width != 32 || p.Height != 3 || p.Stride != 4 || p.XDPI != 300 {
		t.Fatalf("unexpected page %+v", p)
	}
	if !bytes.Equal(p.Data, bytes.Join(rows, nil)) {
		t.Fatalf("rows = % x", p.Data)
	}
}

// TestScanBWHostSettings covers M=TEXT scans: one PackBits row per 0x42 block,
// as recorded from an MFC-L2800DW.
func TestScanBWHostSettings(t *testing.T) {
	rows := [][]byte{bytes.Repeat([]byte{0}, 6), {1, 2, 3, 4, 5, 6}}
	d := &device{gkp: gkpReply(0x81, nil), stream: func(conn net.Conn) {
		for _, row := range rows {
			enc := page.PackBits(nil, row)
			_, _ = conn.Write([]byte{blockPackBits, blockMagic, 0, 1, 0, 0x84, 0, 0, 0, 0, byte(len(enc)), 0})
			_, _ = conn.Write(enc)
		}
		_, _ = conn.Write([]byte{blockPageEnd, blockMagic, 0, 1, 0, 0x84, 0, 0, 0, 0})
	}}
	pages, _ := collect(t, d.serve(t), Settings{Resolution: 300, Mode: ModeBW})
	if x := <-d.gotX; !strings.Contains(x, "C=RLENGTH\n") || !strings.Contains(x, "M=TEXT\n") {
		t.Fatalf("X message %q", x)
	}
	if len(pages) != 1 || pages[0].Kind != page.Bilevel || pages[0].Stride != 6 || pages[0].Height != 2 || pages[0].XDPI != 300 {
		t.Fatalf("unexpected pages %+v", pages)
	}
	if !bytes.Equal(pages[0].Data, bytes.Join(rows, nil)) {
		t.Fatalf("rows = % x", pages[0].Data)
	}
}

func TestScanFallsBackToK(t *testing.T) {
	session := append([]byte{withParams, 0x15, 0x30}, []byte("F=FILE\nR=300\n\x80")...)
	d := &device{k: session, stream: classicStream(testJPEG(t, 8, 8))}
	c := New(d.serve(t))
	c.PageTimeout = 5 * time.Second
	c.ProbeTimeout = 100 * time.Millisecond
	res, err := c.Scan(context.Background(), Settings{Resolution: 300}, func(page.Page) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 || res.Session["F"] != "FILE" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestScanNoPages(t *testing.T) {
	d := &device{gkp: gkpReply(0x81, nil), stream: classicStream()}
	c := New(d.serve(t))
	if _, err := c.Scan(context.Background(), Settings{Resolution: 300}, func(page.Page) error { return nil }); err == nil {
		t.Fatal("expected error")
	}
}

func TestMessage(t *testing.T) {
	got := message('I', [][2]string{{"D", "SIN"}, {"J", ""}, {"R", "300,300"}})
	if !bytes.Equal(got, []byte("\x1bI\nD=SIN\nR=300,300\n\x80")) {
		t.Fatalf("got %q", got)
	}
}

func TestParseDimensions(t *testing.T) {
	v, err := parseDimensions([]byte("100,100,2,209,826,291,1145,\x00"))
	if err != nil || v[4] != 826 || v[6] != 1145 {
		t.Fatalf("v=%v err=%v", v, err)
	}
}
