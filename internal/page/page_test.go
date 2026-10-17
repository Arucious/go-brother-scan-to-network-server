package page

import (
	"bytes"
	"testing"
)

func TestPackBitsRoundTrip(t *testing.T) {
	rows := [][]byte{
		bytes.Repeat([]byte{0}, 310),
		append(bytes.Repeat([]byte{0}, 200), []byte{1, 2, 3, 4, 5, 5, 5, 5, 6, 7}...),
		{9},
		{1, 2},
		bytes.Repeat([]byte{1, 2, 3}, 100),
	}
	for i, row := range rows {
		enc := PackBits(nil, row)
		dec, err := UnpackBits(nil, enc)
		if err != nil || !bytes.Equal(dec, row) {
			t.Fatalf("row %d: round trip failed (%v)", i, err)
		}
	}
}

// TestUnpackBitsDeviceRow decodes the start of a row recorded from an
// MFC-L2800DW black & white scan: 128 + 54 white bytes, then literals.
func TestUnpackBitsDeviceRow(t *testing.T) {
	src := append([]byte{0x81, 0x00, 0xcb, 0x00, 0x02}, 0xaa, 0xbb, 0xcc)
	dec, err := UnpackBits(nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(dec) != 128+54+3 || dec[0] != 0 || dec[182] != 0xaa {
		t.Fatalf("decoded %d bytes", len(dec))
	}
}

func TestBilevelImage(t *testing.T) {
	p := Page{Kind: Bilevel, Width: 10, Height: 2, Stride: 2, Data: []byte{0x80, 0x00, 0x00, 0x40}}
	img, err := p.Image()
	if err != nil {
		t.Fatal(err)
	}
	gray := func(x, y int) uint8 { r, _, _, _ := img.At(x, y).RGBA(); return uint8(r >> 8) }
	if gray(0, 0) != 0 || gray(1, 0) != 0xff || gray(9, 1) != 0 || gray(8, 1) != 0xff {
		t.Fatal("unexpected pixels")
	}
}
