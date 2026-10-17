package pdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"regexp"
	"strconv"
	"testing"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

func makePage(t *testing.T, img image.Image) page.Page {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	p, err := page.NewJPEG(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWrite(t *testing.T) {
	rgb := image.NewRGBA(image.Rect(0, 0, 300, 600))
	rgb.Set(1, 1, color.RGBA{255, 0, 0, 255})
	gray := makePage(t, image.NewGray(image.Rect(0, 0, 600, 300)))
	gray.XDPI, gray.YDPI = 600, 600
	bilevel := page.Page{Kind: page.Bilevel, Width: 16, Height: 2, Stride: 2, Data: []byte{0xff, 0, 0, 0xff}, XDPI: 8, YDPI: 1}

	var out bytes.Buffer
	if err := Write(&out, []page.Page{makePage(t, rgb), gray, bilevel}, 300); err != nil {
		t.Fatal(err)
	}
	doc := out.Bytes()

	for _, want := range []string{
		"%PDF-1.4", "/Count 3", "/DeviceRGB", "/DeviceGray /BitsPerComponent 8",
		"/MediaBox [0 0 72.00 144.00]", // 300x600 at fallback 300 dpi
		"/MediaBox [0 0 72.00 36.00]",  // 600x300 at its own 600 dpi
		"/MediaBox [0 0 144.00 144.00]", "/BitsPerComponent 1 /Decode [1 0] /Filter /FlateDecode",
		"%%EOF",
	} {
		if !bytes.Contains(doc, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}

	// Every xref offset must point at the matching "N 0 obj".
	m := regexp.MustCompile(`startxref\n(\d+)`).FindSubmatch(doc)
	if m == nil {
		t.Fatal("no startxref")
	}
	xref, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(doc[xref:], []byte("xref\n")) {
		t.Fatalf("startxref %d does not point at xref table", xref)
	}
	entries := regexp.MustCompile(`(\d{10}) 00000 n `).FindAllSubmatch(doc[xref:], -1)
	if len(entries) != 12 {
		t.Fatalf("expected 12 objects, got %d", len(entries))
	}
	for i, e := range entries {
		off, _ := strconv.Atoi(string(e[1]))
		want := strconv.Itoa(i+1) + " 0 obj"
		if !bytes.HasPrefix(doc[off:], []byte(want)) {
			t.Errorf("object %d offset %d points at %q", i+1, off, doc[off:off+10])
		}
	}
}

func TestWriteEmpty(t *testing.T) {
	if err := Write(&bytes.Buffer{}, nil, 300); err == nil {
		t.Fatal("expected error")
	}
}
