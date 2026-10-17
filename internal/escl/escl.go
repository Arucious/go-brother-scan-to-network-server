// Package escl is a minimal eSCL (AirScan) client: enough to pull scanned
// pages from a networked scanner.
package escl

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Input sources.
const (
	SourcePlaten = "Platen"
	SourceFeeder = "Feeder"
)

// Color modes.
const (
	ColorRGB  = "RGB24"
	ColorGray = "Grayscale8"
)

// ADF states reported by ScannerStatus.
const AdfLoaded = "ScannerAdfLoaded"

// Client talks to one scanner.
type Client struct {
	// BaseURL is the eSCL root, e.g. http://192.168.1.10/eSCL.
	BaseURL string
	HTTP    *http.Client
}

// New returns a client for the scanner at address (host or host:port).
func New(address string) *Client {
	return &Client{
		BaseURL: "http://" + address + "/eSCL",
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
	}
}

// Capabilities is the subset of ScannerCapabilities this client uses.
type Capabilities struct {
	Version      string     `xml:"Version"`
	MakeAndModel string     `xml:"MakeAndModel"`
	Platen       *inputCaps `xml:"Platen>PlatenInputCaps"`
	Adf          *inputCaps `xml:"Adf>AdfSimplexInputCaps"`
}

type inputCaps struct {
	MaxWidth  int `xml:"MaxWidth"`
	MaxHeight int `xml:"MaxHeight"`
}

// MaxSize returns the maximum scan area for source in 1/300 inch.
func (c *Capabilities) MaxSize(source string) (width, height int) {
	caps := c.Platen
	if source == SourceFeeder && c.Adf != nil {
		caps = c.Adf
	}
	if caps == nil {
		return 0, 0
	}
	return caps.MaxWidth, caps.MaxHeight
}

// Status is the subset of ScannerStatus this client uses.
type Status struct {
	State    string `xml:"State"`
	AdfState string `xml:"AdfState"`
}

// Capabilities fetches ScannerCapabilities.
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	var caps Capabilities
	if err := c.getXML(ctx, "/ScannerCapabilities", &caps); err != nil {
		return nil, err
	}
	return &caps, nil
}

// Status fetches ScannerStatus.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.getXML(ctx, "/ScannerStatus", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) getXML(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("escl: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("escl: GET %s: %s", path, resp.Status)
	}
	if err := xml.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("escl: decode %s: %w", path, err)
	}
	return nil
}

// Settings describes one scan job.
type Settings struct {
	Source     string // SourcePlaten or SourceFeeder
	ColorMode  string // ColorRGB or ColorGray
	Resolution int    // dpi
	Width      int    // 1/300 inch
	Height     int    // 1/300 inch
	Intent     string // Document, Photo, TextAndGraphic
	Format     string // MIME type, e.g. image/jpeg
}

// XML renders the ScanSettings request body.
//
// Element order and the announced version matter: the Brother MFC-L2800DW
// answers HTTP 500, or silently falls back to its panel defaults (200 dpi
// color), for other orderings and for its own advertised version (2.93). This
// layout matches sane-airscan, which works across many devices.
func (s Settings) XML() []byte {
	intent := s.Intent
	if intent == "" {
		intent = "Document"
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<scan:ScanSettings xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm" xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03">`)
	b.WriteString(`<pwg:Version>2.0</pwg:Version>`)
	fmt.Fprintf(&b, `<scan:Intent>%s</scan:Intent>`, xmlEscape(intent))
	b.WriteString(`<pwg:ScanRegions><pwg:ScanRegion>`)
	b.WriteString(`<pwg:ContentRegionUnits>escl:ThreeHundredthsOfInches</pwg:ContentRegionUnits>`)
	b.WriteString(`<pwg:XOffset>0</pwg:XOffset><pwg:YOffset>0</pwg:YOffset>`)
	fmt.Fprintf(&b, `<pwg:Width>%d</pwg:Width><pwg:Height>%d</pwg:Height>`, s.Width, s.Height)
	b.WriteString(`</pwg:ScanRegion></pwg:ScanRegions>`)
	fmt.Fprintf(&b, `<pwg:InputSource>%s</pwg:InputSource>`, xmlEscape(s.Source))
	fmt.Fprintf(&b, `<scan:ColorMode>%s</scan:ColorMode>`, xmlEscape(s.ColorMode))
	fmt.Fprintf(&b, `<pwg:DocumentFormat>%s</pwg:DocumentFormat>`, xmlEscape(s.Format))
	fmt.Fprintf(&b, `<scan:DocumentFormatExt>%s</scan:DocumentFormatExt>`, xmlEscape(s.Format))
	fmt.Fprintf(&b, `<scan:XResolution>%d</scan:XResolution><scan:YResolution>%d</scan:YResolution>`, s.Resolution, s.Resolution)
	b.WriteString(`</scan:ScanSettings>`)
	return b.Bytes()
}

// feederLoadDelay is waited between feeder pages. Brother devices answer 404
// (ending the job early) when the next page is requested immediately; see
// sane-airscan's quirk_next_load_delay.
var feederLoadDelay = time.Second

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ErrBusy is returned when the scanner keeps refusing a job.
var ErrBusy = errors.New("escl: scanner busy")

// Scan runs one job and calls onPage for each page as it arrives.
func (c *Client) Scan(ctx context.Context, s Settings, onPage func(page []byte) error) (int, error) {
	job, err := c.createJob(ctx, s)
	if err != nil {
		return 0, err
	}

	pages := 0
	retries := 0
	for {
		data, status, err := c.nextDocument(ctx, job)
		if err != nil {
			c.cancel(job)
			return pages, err
		}
		switch status {
		case http.StatusOK:
			retries = 0
			pages++
			if err := onPage(data); err != nil {
				c.cancel(job)
				return pages, err
			}
			if s.Source == SourceFeeder {
				if err := sleep(ctx, feederLoadDelay); err != nil {
					c.cancel(job)
					return pages, err
				}
			}
		case http.StatusNotFound, http.StatusGone:
			// No more pages.
			if pages == 0 {
				return 0, fmt.Errorf("escl: job finished without pages (is a document loaded?)")
			}
			return pages, nil
		case http.StatusServiceUnavailable:
			// Page still being scanned.
			retries++
			if retries > 120 {
				c.cancel(job)
				return pages, fmt.Errorf("escl: timed out waiting for page %d", pages+1)
			}
			if err := sleep(ctx, time.Second); err != nil {
				c.cancel(job)
				return pages, err
			}
		default:
			c.cancel(job)
			return pages, fmt.Errorf("escl: NextDocument returned HTTP %d", status)
		}
	}
}

func (c *Client) createJob(ctx context.Context, s Settings) (string, error) {
	body := s.XML()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/ScanJobs", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "text/xml")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return "", fmt.Errorf("escl: create job: %w", err)
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusCreated, http.StatusOK:
			loc := resp.Header.Get("Location")
			if loc == "" {
				return "", fmt.Errorf("escl: create job: no Location header")
			}
			return c.resolve(loc)
		case http.StatusServiceUnavailable, http.StatusConflict:
			if attempt >= 30 {
				return "", ErrBusy
			}
			if err := sleep(ctx, 2*time.Second); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("escl: create job: HTTP %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
	}
}

// resolve turns a Location header into an absolute job URL.
func (c *Client) resolve(loc string) (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("escl: bad job location %q: %w", loc, err)
	}
	return strings.TrimSuffix(base.ResolveReference(ref).String(), "/"), nil
}

func (c *Client) nextDocument(ctx context.Context, job string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, job+"/NextDocument", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("escl: next document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("escl: read page: %w", err)
	}
	return data, resp.StatusCode, nil
}

// cancel deletes a job; errors are ignored because the job may already be gone.
func (c *Client) cancel(job string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, job, nil)
	if err != nil {
		return
	}
	if resp, err := c.HTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
