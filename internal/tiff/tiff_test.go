package tiff

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
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

func TestParseJPEG(t *testing.T) {
	info, err := ParseJPEG(makePage(t, image.NewRGBA(image.Rect(0, 0, 32, 16))).Data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 32 || info.Height != 16 || info.Components != 3 || info.HSamp != 2 || info.VSamp != 2 {
		t.Fatalf("unexpected info %+v", info)
	}
	if _, err := ParseJPEG([]byte("nope")); err == nil {
		t.Fatal("expected error")
	}
}

// ifds walks the IFD chain and returns tag maps (first value of each tag).
func ifds(t *testing.T, b []byte) []map[uint16]uint32 {
	t.Helper()
	if !bytes.HasPrefix(b, []byte{'I', 'I', 42, 0}) {
		t.Fatal("bad header")
	}
	var out []map[uint16]uint32
	off := binary.LittleEndian.Uint32(b[4:])
	for off != 0 {
		n := binary.LittleEndian.Uint16(b[off:])
		tags := map[uint16]uint32{}
		for i := range int(n) {
			e := b[int(off)+2+12*i:]
			tag, typ := binary.LittleEndian.Uint16(e), binary.LittleEndian.Uint16(e[2:])
			if typ == typeShort {
				tags[tag] = uint32(binary.LittleEndian.Uint16(e[8:]))
			} else {
				tags[tag] = binary.LittleEndian.Uint32(e[8:])
			}
		}
		out = append(out, tags)
		off = binary.LittleEndian.Uint32(b[int(off)+2+12*int(n):])
	}
	return out
}

func TestWrite(t *testing.T) {
	color := makePage(t, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	gray := makePage(t, image.NewGray(image.Rect(0, 0, 10, 30)))
	bilevel := page.Page{Kind: page.Bilevel, Width: 16, Height: 2, Stride: 2, Data: []byte{0xff, 0xff, 0x00, 0x0f}}
	var out bytes.Buffer
	if err := Write(&out, []page.Page{color, gray, bilevel}, 200); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	pages := ifds(t, b)
	if len(pages) != 3 {
		t.Fatalf("expected 3 IFDs, got %d", len(pages))
	}
	checks := []struct {
		w, h, compression, photometric uint32
	}{{40, 20, 7, 6}, {10, 30, 7, 1}, {16, 2, compressionPackBits, 0}}
	for i, c := range checks {
		p := pages[i]
		if p[tagImageWidth] != c.w || p[tagImageLength] != c.h || p[tagCompression] != c.compression || p[tagPhotometric] != c.photometric {
			t.Errorf("page %d tags %v", i+1, p)
		}
		if p[tagPageNumber] != uint32(i) {
			t.Errorf("page %d PageNumber = %d", i+1, p[tagPageNumber])
		}
	}
	for i, src := range []page.Page{color, gray} {
		off, n := pages[i][tagStripOffsets], pages[i][tagStripByteCounts]
		if !bytes.Equal(b[off:off+n], src.Data) {
			t.Errorf("page %d strip does not hold the original JPEG", i+1)
		}
	}
	off, n := pages[2][tagStripOffsets], pages[2][tagStripByteCounts]
	rows, err := page.UnpackBits(nil, b[off:off+n])
	if err != nil || !bytes.Equal(rows, bilevel.Data) {
		t.Errorf("bilevel strip decodes to % x (%v)", rows, err)
	}
}
