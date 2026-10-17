package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brother"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brscan"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/destination"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/escl"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/render"
)

// sourcePanel marks scans made with the settings chosen on the device panel.
const sourcePanel = "panel"

// outcome is the result of scanAndStore.
type outcome struct {
	settings JobSettings
	pages    int
	files    []destination.Saved
}

func (s *Service) scanAndStore(ctx context.Context, cfg *config.Config, target *config.Target, fn brother.Function, p config.Profile, custom bool, log *slog.Logger) (outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	var res outcome
	res.settings.From = "config"
	if custom {
		res.settings.From = "custom"
	}

	var scanned []page.Page
	var panel *brscan.PanelSettings
	var source string
	var err error
	if cfg.Printer.ScanProtocol == "escl" {
		scanned, source, err = scanESCL(ctx, cfg, fn, p, log)
	} else {
		scanned, source, panel, err = scanNative(ctx, cfg, p, log)
	}
	res.pages = len(scanned)
	res.settings.Source = source
	if source == sourcePanel {
		res.settings.From = sourcePanel
		res.settings.Source = "auto"
	}
	if err != nil {
		return res, err
	}

	// With panel settings the device already scanned in the chosen color mode.
	// Otherwise normalise JPEGs: Brother sends gray as three channels, and
	// eSCL has no 1-bit JPEG, so black & white is thresholded from gray.
	if source != sourcePanel && (p.Color == "gray" || p.Color == "bw") {
		convert, what := toGray, "grayscale"
		if p.Color == "bw" {
			convert, what = toBilevel, "black & white"
		}
		for i, pg := range scanned {
			if out, err := convert(pg); err == nil {
				scanned[i] = out
			} else {
				log.Warn("could not convert page to "+what, "page", i+1, "err", err)
			}
		}
	}

	out, err := render.FromConfig(p.Format)
	if err != nil {
		return res, err
	}
	res.settings.FileType = fileTypeLabel(out)
	if panel != nil {
		if po, warn, ok := panelOutput(*panel); ok {
			out = po
			res.settings.FileType = fileTypeLabel(out)
			if name := panel.FileType(); warn != "" && name != "" {
				res.settings.FileType += " (panel: " + name + ")"
				log.Warn(warn)
			}
		} else if warn != "" {
			log.Warn(warn, "format", p.Format)
		}
	}
	describePages(&res.settings, scanned, p.Resolution)

	base, err := target.RenderFilename(config.FilenameData{Time: time.Now(), Target: target.Name, Function: fn.Lower()})
	if err != nil {
		return res, fmt.Errorf("render filename: %w", err)
	}
	rendered, err := render.Render(scanned, out, base, p.Resolution)
	if err != nil {
		return res, err
	}
	outFiles := make([]destination.File, len(rendered))
	for i, f := range rendered {
		outFiles[i] = destination.File{Name: f.Name, Data: f.Data}
	}

	dest, err := destination.New(target.Destination)
	if err != nil {
		return res, err
	}
	res.files, err = dest.Save(ctx, outFiles)
	return res, err
}

// fileTypeLabel describes an output for people.
func fileTypeLabel(o render.Output) string {
	name := map[string]string{"pdf": "PDF", "tiff": "TIFF", "jpeg": "JPEG", "png": "PNG", "bmp": "BMP"}[o.Format]
	switch {
	case o.Format != "pdf" && o.Format != "tiff":
		return name
	case o.Split == 1:
		return name + ", one file per page"
	case o.Split > 1:
		return fmt.Sprintf("%s, every %d pages", name, o.Split)
	}
	return name + ", multi-page"
}

// describePages fills color, resolution and page size from the scanned pages.
func describePages(st *JobSettings, pages []page.Page, fallbackDPI int) {
	if len(pages) == 0 {
		return
	}
	st.Color = "gray"
	for _, p := range pages {
		switch {
		case p.Kind == page.Bilevel:
			st.Color = "black & white"
		case !p.IsGray() && st.Color != "black & white":
			st.Color = "color"
		}
	}
	x, y := pages[0].DPI(fallbackDPI)
	st.DPI = x
	wmm := float64(pages[0].Width) * 25.4 / float64(x)
	hmm := float64(pages[0].Height) * 25.4 / float64(y)
	st.PageSize = fmt.Sprintf("%.0f × %.0f mm", wmm, hmm)
	// Scans are cropped slightly inside the paper, so match loosely.
	for _, ps := range []struct {
		name string
		w, h float64
	}{{"A4", 210, 297}, {"Letter", 215.9, 279.4}, {"Legal", 215.9, 355.6}, {"A5", 148, 210}} {
		if wmm > ps.w-12 && wmm < ps.w+4 && hmm > ps.h-12 && hmm < ps.h+4 {
			st.PageSize += " (" + ps.name + ")"
			break
		}
	}
}

// panelOutput maps the panel's file type and split settings to an output.
// ok is false when the panel set no file type; warn explains substitutions.
func panelOutput(p brscan.PanelSettings) (out render.Output, warn string, ok bool) {
	split := 0
	switch p.SplitType {
	case 1:
		split = 1
	case 4:
		split = int(p.SplitPages)
	}
	switch p.SaveFileType {
	case 0xff:
		return out, "", false
	case 0x01:
		return render.Output{Format: "pdf", Split: split}, "", true
	case 0x02, 0x03, 0x04:
		return render.Output{Format: "pdf", Split: split},
			fmt.Sprintf("panel file type %s is not supported; writing a plain PDF", p.FileType()), true
	case 0x11:
		return render.Output{Format: "tiff", Split: 1}, "", true
	case 0x12:
		return render.Output{Format: "tiff", Split: split}, "", true
	case 0x21:
		return render.Output{Format: "jpeg", Split: 1}, "", true
	case 0x22:
		return render.Output{Format: "png", Split: 1}, "", true
	case 0x23:
		return render.Output{Format: "bmp", Split: 1}, "", true
	case 0x24, 0x41, 0x42, 0x43:
		return render.Output{Format: "pdf", Split: split},
			fmt.Sprintf("panel file type %s is not supported; writing PDF instead", p.FileType()), true
	}
	return out, fmt.Sprintf("unknown panel file type 0x%02x; using the configured format", p.SaveFileType), false
}

// scanNative pulls pages over Brother's native protocol. The device picks the
// feeder automatically when paper is loaded.
func scanNative(ctx context.Context, cfg *config.Config, p config.Profile, log *slog.Logger) ([]page.Page, string, *brscan.PanelSettings, error) {
	settings := brscan.Settings{Resolution: p.Resolution, Mode: brscan.ModeColor}
	switch p.Color {
	case "gray":
		settings.Mode = brscan.ModeGray
	case "bw":
		settings.Mode = brscan.ModeBW
	}
	if size, ok := config.PageSizes[p.PageSize]; ok {
		settings.Width = size[0] * p.Resolution / 300
		settings.Height = size[1] * p.Resolution / 300
	}

	client := brscan.New(cfg.Printer.Address)
	// BRSCAN_CAPTURE_DIR saves the raw device stream for protocol debugging.
	if dir := os.Getenv("BRSCAN_CAPTURE_DIR"); dir != "" {
		name := filepath.Join(dir, fmt.Sprintf("brscan-%s.bin", time.Now().Format("20060102-150405")))
		if f, err := os.Create(name); err == nil {
			defer f.Close()
			client.Capture = f
			log.Info("capturing raw scan stream", "file", name)
		}
	}

	log.Info("scanning", "protocol", "native", "color", p.Color, "dpi", p.Resolution)
	var pages []page.Page
	res, err := client.Scan(ctx, settings, func(pg page.Page) error {
		pages = append(pages, pg)
		log.Info("page received", "page", len(pages), "bytes", len(pg.Data), "size", fmt.Sprintf("%dx%d", pg.Width, pg.Height), "dpi", pg.XDPI)
		return nil
	})
	if res.Panel != nil {
		log.Info("panel scan settings", "settings", res.Panel.String())
	}
	if len(res.Session) > 0 {
		log.Info("push scan session", "params", res.Session)
	}
	source := "auto"
	if res.DeviceSettings {
		source = sourcePanel
	}
	return pages, source, res.Panel, err
}

// scanESCL pulls pages over eSCL. Devices refuse eSCL while a button-started
// push session is waiting, so this only suits web-triggered scans.
func scanESCL(ctx context.Context, cfg *config.Config, fn brother.Function, p config.Profile, log *slog.Logger) ([]page.Page, string, error) {
	esclAddr := cfg.Printer.ESCLAddress
	if esclAddr == "" {
		esclAddr = cfg.Printer.Address
	}
	client := escl.New(esclAddr)

	caps, err := client.Capabilities(ctx)
	if err != nil {
		return nil, "", err
	}

	source := escl.SourcePlaten
	switch p.Source {
	case "adf":
		source = escl.SourceFeeder
	case "auto":
		if st, err := client.Status(ctx); err == nil && st.AdfState == escl.AdfLoaded && caps.Adf != nil {
			source = escl.SourceFeeder
		}
	}

	maxW, maxH := caps.MaxSize(source)
	width, height := maxW, maxH
	if size, ok := config.PageSizes[p.PageSize]; ok {
		width, height = min(size[0], maxW), min(size[1], maxH)
	}
	if width == 0 || height == 0 {
		width, height = config.PageSizes["a4"][0], config.PageSizes["a4"][1]
	}

	settings := escl.Settings{
		Source:     source,
		ColorMode:  escl.ColorRGB,
		Resolution: p.Resolution,
		Width:      width,
		Height:     height,
		Intent:     "Document",
		Format:     "image/jpeg",
	}
	if p.Color == "gray" || p.Color == "bw" {
		settings.ColorMode = escl.ColorGray
	}
	if fn == brother.FuncImage {
		settings.Intent = "Photo"
	}

	log.Info("scanning", "protocol", "escl", "source", source, "color", p.Color, "dpi", p.Resolution)
	var pages []page.Page
	_, err = client.Scan(ctx, settings, func(data []byte) error {
		pg, err := page.NewJPEG(data)
		if err != nil {
			return err
		}
		if pg.XDPI == 0 {
			pg.XDPI, pg.YDPI = p.Resolution, p.Resolution
		}
		pages = append(pages, pg)
		log.Info("page received", "page", len(pages), "bytes", len(data))
		return nil
	})
	return pages, source, err
}

// toGray re-encodes a color JPEG page as single-channel grayscale. Brother
// devices return gray scans as three-channel JPEGs.
func toGray(p page.Page) (page.Page, error) {
	if p.Kind != page.JPEG || p.IsGray() {
		return p, nil
	}
	img, err := jpeg.Decode(bytes.NewReader(p.Data))
	if err != nil {
		return p, err
	}
	gray := image.NewGray(img.Bounds())
	draw.Draw(gray, gray.Bounds(), img, img.Bounds().Min, draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gray, &jpeg.Options{Quality: 90}); err != nil {
		return p, err
	}
	p.Data = buf.Bytes()
	return p, nil
}

// toBilevel thresholds a JPEG page to 1-bit black & white.
func toBilevel(p page.Page) (page.Page, error) {
	if p.Kind != page.JPEG {
		return p, nil
	}
	img, err := jpeg.Decode(bytes.NewReader(p.Data))
	if err != nil {
		return p, err
	}
	b := img.Bounds()
	gray := image.NewGray(b)
	draw.Draw(gray, b, img, b.Min, draw.Src)
	stride := (b.Dx() + 7) / 8
	bits := make([]byte, stride*b.Dy())
	for y := range b.Dy() {
		for x := range b.Dx() {
			if gray.Pix[y*gray.Stride+x] < 128 {
				bits[y*stride+x>>3] |= 0x80 >> (x & 7)
			}
		}
	}
	return page.Page{Kind: page.Bilevel, Data: bits, Width: b.Dx(), Height: b.Dy(), Stride: stride, XDPI: p.XDPI, YDPI: p.YDPI}, nil
}
