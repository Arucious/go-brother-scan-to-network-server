// Package page holds scanned pages as delivered by the scanner.
package page

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
)

// Kind is the encoding of a page.
type Kind int

const (
	// JPEG pages hold a complete JPEG file (color or gray).
	JPEG Kind = iota
	// Bilevel pages hold uncompressed 1-bit rows, MSB first, 1 = black.
	Bilevel
)

// Page is one scanned page.
type Page struct {
	Kind Kind
	// Data is the JPEG file (JPEG) or the packed rows (Bilevel).
	Data []byte
	// Width and Height in pixels; Stride is bytes per row (Bilevel only).
	Width, Height, Stride int
	// XDPI and YDPI are the scan resolution; zero means unknown.
	XDPI, YDPI int
}

// NewJPEG wraps a JPEG file, reading its size and JFIF density.
func NewJPEG(data []byte) (Page, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Page{}, err
	}
	p := Page{Kind: JPEG, Data: data, Width: cfg.Width, Height: cfg.Height}
	p.XDPI, p.YDPI, _ = JFIFDensity(data)
	return p, nil
}

// DPI returns the page resolution, or fallback when unknown.
func (p Page) DPI(fallback int) (x, y int) {
	if p.XDPI > 0 && p.YDPI > 0 {
		return p.XDPI, p.YDPI
	}
	return fallback, fallback
}

// Image decodes the page.
func (p Page) Image() (image.Image, error) {
	switch p.Kind {
	case JPEG:
		return jpeg.Decode(bytes.NewReader(p.Data))
	case Bilevel:
		if p.Stride*p.Height > len(p.Data) {
			return nil, errors.New("page: bilevel data too short")
		}
		img := image.NewGray(image.Rect(0, 0, p.Width, p.Height))
		for y := range p.Height {
			row := p.Data[y*p.Stride:]
			for x := range p.Width {
				if row[x>>3]&(0x80>>(x&7)) == 0 {
					img.Pix[y*img.Stride+x] = 0xff
				}
			}
		}
		return img, nil
	}
	return nil, errors.New("page: unknown kind")
}

// IsGray reports whether a JPEG page has a single channel.
func (p Page) IsGray() bool {
	if p.Kind == Bilevel {
		return true
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Data))
	return err == nil && cfg.ColorModel == color.GrayModel
}

// JFIFDensity returns the pixel density in dpi from a JPEG's JFIF APP0
// segment. ok is false when the segment is missing or has no physical units.
func JFIFDensity(data []byte) (xdpi, ydpi int, ok bool) {
	// FF D8 FF E0 len:u16 "JFIF\0" version:u16 units:u8 x:u16 y:u16
	if len(data) < 18 || !bytes.Equal(data[:4], []byte{0xff, 0xd8, 0xff, 0xe0}) || string(data[6:11]) != "JFIF\x00" {
		return 0, 0, false
	}
	x := int(data[14])<<8 | int(data[15])
	y := int(data[16])<<8 | int(data[17])
	if x == 0 || y == 0 {
		return 0, 0, false
	}
	switch data[13] {
	case 1: // dots per inch
		return x, y, true
	case 2: // dots per cm
		return int(float64(x)*2.54 + 0.5), int(float64(y)*2.54 + 0.5), true
	}
	return 0, 0, false
}

// UnpackBits decodes PackBits data, appending to dst.
func UnpackBits(dst, src []byte) ([]byte, error) {
	for i := 0; i < len(src); {
		n := int(int8(src[i]))
		i++
		switch {
		case n >= 0: // n+1 literal bytes
			if i+n+1 > len(src) {
				return dst, errors.New("page: truncated PackBits literal")
			}
			dst = append(dst, src[i:i+n+1]...)
			i += n + 1
		case n == -128: // no-op
		default: // repeat next byte 1-n times
			if i >= len(src) {
				return dst, errors.New("page: truncated PackBits run")
			}
			for range 1 - n {
				dst = append(dst, src[i])
			}
			i++
		}
	}
	return dst, nil
}

// PackBits encodes one row with PackBits, appending to dst.
func PackBits(dst, row []byte) []byte {
	for i := 0; i < len(row); {
		// Run of identical bytes.
		j := i + 1
		for j < len(row) && j-i < 128 && row[j] == row[i] {
			j++
		}
		if j-i >= 2 {
			dst = append(dst, byte(int8(1-(j-i))), row[i])
			i = j
			continue
		}
		// Literal run until the next repeat of 3+ or 128 bytes.
		j = i
		for j < len(row) && j-i < 128 {
			if j+2 < len(row) && row[j] == row[j+1] && row[j] == row[j+2] {
				break
			}
			j++
		}
		dst = append(dst, byte(j-i-1))
		dst = append(dst, row[i:j]...)
		i = j
	}
	return dst
}
