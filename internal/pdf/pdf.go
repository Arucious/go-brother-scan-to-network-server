// Package pdf writes multi-page PDFs from scanned pages. JPEG pages are
// embedded without re-encoding; bilevel pages become 1-bit Flate images.
package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

// Write writes one PDF page per scanned page. fallbackDPI sizes pages whose
// resolution is unknown.
func Write(w io.Writer, pages []page.Page, fallbackDPI int) error {
	if len(pages) == 0 {
		return fmt.Errorf("pdf: no pages")
	}
	if fallbackDPI <= 0 {
		fallbackDPI = 300
	}

	pw := &writer{w: w}
	pw.printf("%%PDF-1.4\n%%\xe2\xe3\xcf\xd3\n")

	// Object layout: 1 catalog, 2 pages, 3 info, then 3 objects per page
	// (page, content stream, image).
	const firstPageObj = 4
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", firstPageObj+i*3)
	}

	pw.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	pw.object(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", join(kids), len(pages)))
	pw.object(3, fmt.Sprintf("<< /Producer (go-brother-scan-to-network-server) /CreationDate (D:%s) >>",
		time.Now().UTC().Format("20060102150405Z")))

	for i, p := range pages {
		dict, data, err := imageObject(p)
		if err != nil {
			return fmt.Errorf("pdf: page %d: %w", i+1, err)
		}

		pageObj := firstPageObj + i*3
		contentObj := pageObj + 1
		imageObj := pageObj + 2

		xdpi, ydpi := p.DPI(fallbackDPI)
		wPt := float64(p.Width) * 72 / float64(xdpi)
		hPt := float64(p.Height) * 72 / float64(ydpi)

		pw.object(pageObj, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			wPt, hPt, imageObj, contentObj))

		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q\n", wPt, hPt)
		pw.stream(contentObj, fmt.Sprintf("<< /Length %d >>", len(content)), []byte(content))
		pw.stream(imageObj, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d %s /Length %d >>",
			p.Width, p.Height, dict, len(data)), data)
	}

	xref := pw.n
	count := firstPageObj + len(pages)*3
	pw.printf("xref\n0 %d\n0000000000 65535 f \n", count)
	for i := 1; i < count; i++ {
		pw.printf("%010d 00000 n \n", pw.offsets[i])
	}
	pw.printf("trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", count, xref)
	return pw.err
}

// imageObject returns the image dictionary entries and stream data for a page.
func imageObject(p page.Page) (dict string, data []byte, err error) {
	switch p.Kind {
	case page.JPEG:
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Data))
		if err != nil {
			return "", nil, err
		}
		colorSpace, decode := "/DeviceRGB", ""
		switch cfg.ColorModel {
		case color.GrayModel, color.Gray16Model:
			colorSpace = "/DeviceGray"
		case color.CMYKModel:
			// Adobe CMYK JPEGs are stored inverted.
			colorSpace, decode = "/DeviceCMYK", " /Decode [1 0 1 0 1 0 1 0]"
		}
		return fmt.Sprintf("/ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode%s", colorSpace, decode), p.Data, nil
	case page.Bilevel:
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		for y := range p.Height {
			row := p.Data[y*p.Stride : (y+1)*p.Stride]
			if _, err := zw.Write(row[:(p.Width+7)/8]); err != nil {
				return "", nil, err
			}
		}
		if err := zw.Close(); err != nil {
			return "", nil, err
		}
		// Rows store 1 = black; DeviceGray 1 is white, so invert.
		return "/ColorSpace /DeviceGray /BitsPerComponent 1 /Decode [1 0] /Filter /FlateDecode", buf.Bytes(), nil
	}
	return "", nil, fmt.Errorf("unsupported page kind %d", p.Kind)
}

func join(s []string) string {
	var b bytes.Buffer
	for i, v := range s {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(v)
	}
	return b.String()
}

type writer struct {
	w       io.Writer
	n       int
	offsets map[int]int
	err     error
}

func (p *writer) write(b []byte) {
	if p.err != nil {
		return
	}
	n, err := p.w.Write(b)
	p.n += n
	p.err = err
}

func (p *writer) printf(format string, args ...any) {
	p.write(fmt.Appendf(nil, format, args...))
}

func (p *writer) begin(id int) {
	if p.offsets == nil {
		p.offsets = map[int]int{}
	}
	p.offsets[id] = p.n
	p.printf("%d 0 obj\n", id)
}

func (p *writer) object(id int, dict string) {
	p.begin(id)
	p.printf("%s\nendobj\n", dict)
}

func (p *writer) stream(id int, dict string, data []byte) {
	p.begin(id)
	p.printf("%s\nstream\n", dict)
	p.write(data)
	p.printf("\nendstream\nendobj\n")
}
