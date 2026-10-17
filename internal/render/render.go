// Package render turns scanned pages into output files.
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/bmp"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/pdf"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/tiff"
)

// Output selects the file format and how pages are split into files.
type Output struct {
	// Format is pdf, tiff, jpeg, png or bmp.
	Format string
	// Split is the number of pages per file; 0 puts all pages in one file.
	// Image formats always write one file per page.
	Split int
}

// File is one rendered file; Name is relative and without directory changes.
type File struct {
	Name string
	Data []byte
}

// FromConfig maps a configured format name to an Output.
func FromConfig(format string) (Output, error) {
	switch format {
	case "pdf", "tiff":
		return Output{Format: format}, nil
	case "pdf-single":
		return Output{Format: "pdf", Split: 1}, nil
	case "tiff-single":
		return Output{Format: "tiff", Split: 1}, nil
	case "jpeg", "png", "bmp":
		return Output{Format: format, Split: 1}, nil
	}
	return Output{}, fmt.Errorf("unsupported output format %q", format)
}

var extensions = map[string]string{"pdf": ".pdf", "tiff": ".tif", "jpeg": ".jpg", "png": ".png", "bmp": ".bmp"}

// Render writes pages as files named base[-NNN].ext. fallbackDPI sizes pages
// with unknown resolution.
func Render(pages []page.Page, out Output, base string, fallbackDPI int) ([]File, error) {
	ext, ok := extensions[out.Format]
	if !ok {
		return nil, fmt.Errorf("unsupported output format %q", out.Format)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages")
	}
	split := out.Split
	if out.Format == "jpeg" || out.Format == "png" || out.Format == "bmp" {
		split = 1
	}
	if split <= 0 || split > len(pages) {
		split = len(pages)
	}

	var groups [][]page.Page
	for i := 0; i < len(pages); i += split {
		groups = append(groups, pages[i:min(i+split, len(pages))])
	}

	files := make([]File, 0, len(groups))
	for i, group := range groups {
		name := base + ext
		if len(groups) > 1 {
			name = fmt.Sprintf("%s-%03d%s", base, i+1, ext)
		}
		data, err := encode(out.Format, group, fallbackDPI)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: name, Data: data})
	}
	return files, nil
}

func encode(format string, pages []page.Page, fallbackDPI int) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case "pdf":
		err = pdf.Write(&buf, pages, fallbackDPI)
	case "tiff":
		err = tiff.Write(&buf, pages, fallbackDPI)
	case "jpeg":
		p := pages[0]
		if p.Kind == page.JPEG {
			return p.Data, nil
		}
		var img image.Image
		if img, err = p.Image(); err == nil {
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
		}
	case "png":
		var img image.Image
		if img, err = imageFor(pages[0]); err == nil {
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
		}
	case "bmp":
		var img image.Image
		if img, err = imageFor(pages[0]); err == nil {
			err = bmp.Encode(&buf, img)
		}
	default:
		err = fmt.Errorf("unsupported output format %q", format)
	}
	return buf.Bytes(), err
}

// imageFor decodes a page for PNG/BMP. Bilevel pages become a two-color
// palette so PNG stores them at one bit per pixel; color JPEGs are converted
// to RGBA because the PNG encoder writes other color models at 16 bits.
func imageFor(p page.Page) (image.Image, error) {
	if p.Kind != page.Bilevel {
		img, err := p.Image()
		if err != nil {
			return nil, err
		}
		if _, ok := img.(*image.Gray); ok {
			return img, nil
		}
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		return rgba, nil
	}
	img := image.NewPaletted(image.Rect(0, 0, p.Width, p.Height), color.Palette{color.White, color.Black})
	for y := range p.Height {
		row := p.Data[y*p.Stride:]
		for x := range p.Width {
			if row[x>>3]&(0x80>>(x&7)) != 0 {
				img.Pix[y*img.Stride+x] = 1
			}
		}
	}
	return img, nil
}
