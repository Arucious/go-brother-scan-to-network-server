package render

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

func jpegPage(t *testing.T) page.Page {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 20, 30)), nil); err != nil {
		t.Fatal(err)
	}
	p, err := page.NewJPEG(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func names(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

func TestRender(t *testing.T) {
	pages := []page.Page{jpegPage(t), jpegPage(t), jpegPage(t)}
	cases := []struct {
		out  Output
		want []string
	}{
		{Output{Format: "pdf"}, []string{"scan.pdf"}},
		{Output{Format: "tiff"}, []string{"scan.tif"}},
		{Output{Format: "pdf", Split: 1}, []string{"scan-001.pdf", "scan-002.pdf", "scan-003.pdf"}},
		{Output{Format: "pdf", Split: 2}, []string{"scan-001.pdf", "scan-002.pdf"}},
		{Output{Format: "jpeg"}, []string{"scan-001.jpg", "scan-002.jpg", "scan-003.jpg"}},
		{Output{Format: "png"}, []string{"scan-001.png", "scan-002.png", "scan-003.png"}},
		{Output{Format: "bmp", Split: 0}, []string{"scan-001.bmp", "scan-002.bmp", "scan-003.bmp"}},
	}
	for _, c := range cases {
		files, err := Render(pages, c.out, "scan", 300)
		if err != nil {
			t.Fatalf("%+v: %v", c.out, err)
		}
		if got := names(files); len(got) != len(c.want) || got[0] != c.want[0] || got[len(got)-1] != c.want[len(c.want)-1] {
			t.Errorf("%+v: files %v, want %v", c.out, got, c.want)
		}
		for _, f := range files {
			if len(f.Data) == 0 {
				t.Errorf("%+v: %s is empty", c.out, f.Name)
			}
		}
	}

	files, err := Render(pages[:1], Output{Format: "jpeg"}, "scan", 300)
	if err != nil || len(files) != 1 || files[0].Name != "scan.jpg" || !bytes.Equal(files[0].Data, pages[0].Data) {
		t.Fatalf("single jpeg page: %v %v", err, names(files))
	}
	if _, err := Render(pages, Output{Format: "xps"}, "scan", 300); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestRenderBilevelPNG(t *testing.T) {
	p := page.Page{Kind: page.Bilevel, Width: 8, Height: 1, Stride: 1, Data: []byte{0x81}}
	files, err := Render([]page.Page{p}, Output{Format: "png"}, "bw", 300)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(files[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	black := func(x int) bool { r, _, _, _ := img.At(x, 0).RGBA(); return r == 0 }
	if !black(0) || black(1) || !black(7) {
		t.Fatal("unexpected pixels")
	}
}

func TestRenderColorPNGIs8Bit(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	p, err := page.NewJPEG(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render([]page.Page{p}, Output{Format: "png"}, "c", 300)
	if err != nil {
		t.Fatal(err)
	}
	// IHDR bit depth is byte 24 of a PNG file.
	if depth := files[0].Data[24]; depth != 8 {
		t.Fatalf("PNG bit depth = %d, want 8", depth)
	}
}

func TestFromConfig(t *testing.T) {
	for format, want := range map[string]Output{
		"pdf": {Format: "pdf"}, "pdf-single": {Format: "pdf", Split: 1},
		"tiff-single": {Format: "tiff", Split: 1}, "png": {Format: "png", Split: 1},
	} {
		if got, err := FromConfig(format); err != nil || got != want {
			t.Errorf("%s: %+v %v", format, got, err)
		}
	}
}
