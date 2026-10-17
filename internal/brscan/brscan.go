// Package brscan implements Brother's native network scan protocol (TCP 54921),
// the protocol iPrint&Scan and ControlCenter use.
//
// After a "Scan to PC" button press the device waits for the host to connect
// on this port and refuses eSCL jobs until that session times out, so button
// scans must use this protocol. It also works when the device is idle.
//
// Protocol (scan commands from brother-scand by Dariusz Stojaczyk; GKP and
// the extended framing from Brother's brscan5 libLxBsScanCoreApi; all
// verified on a Brother MFC-L2800DW):
//
//	device: "+OK 200\r\n"                     ("-NG 401\r\n" when busy)
//	host:   ESC "GKP" '\n' 0x80                (get push-scan parameters, v2)
//	device: result:u8 "GKP" len:u32le, then len bytes of tag:u16le size:u16le value
//	        (result 0x81 = no button session; older devices use ESC 'K' instead)
//	host:   ESC 'I' '\n' "D=SIN\nM=CGRAY\nR=300,300\n" 0x80
//	device: 0x00 len 0x00 "xdpi,ydpi,?,width_mm,width_px,height_mm,height_px,"
//	host:   ESC 'X' '\n' "A=0,0,w,h\nB=50\nC=JPEG\nD=SIN\nG=1\nL=128\nM=CGRAY\nN=50\nR=300,300\n" 0x80
//	device: the scan, then a single 0x80 byte.
//
// The scan uses one of two framings. With host settings:
//
//	[id 0x07 0x00 page:u16 ? progress:u16 ?]  id 0x64 JPEG data: len:u16 + bytes
//	                                          id 0x82 end of page
//
// When options were chosen on the panel the device uses its own settings and
// an extended framing:
//
//	00 02 page:u16 mode:u16 len:u32 progress:u32 + len bytes  JPEG data
//	00 01 page:u16 mode:u16 len:u32 progress:u32 + len bytes  one PackBits row (black & white)
//	00 21 page:u16                                            end of page
package brscan

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

// Port is the native scan port.
const Port = 54921

// Color modes.
const (
	ModeColor = "CGRAY"
	ModeGray  = "GRAY64"
	// ModeBW scans 1-bit black & white, sent as PackBits rows.
	ModeBW = "TEXT"
)

const (
	escape     = 0x1b
	msgEnd     = 0x80
	noParams   = 0xd0
	withParams = 0x30

	blockJPEG     = 0x64
	blockRaw      = 0x40
	blockPackBits = 0x42
	blockPageEnd  = 0x82
	blockMagic    = 0x07

	extBilevel = 0x01 // extended framing: one PackBits row
	extData    = 0x02 // extended framing: JPEG data
	extPageEnd = 0x21 // extended framing: end of page
)

// ErrBusy is returned when the device keeps answering "-NG".
var ErrBusy = errors.New("brscan: scanner busy")

// Settings describes one scan.
type Settings struct {
	Resolution int    // dpi
	Mode       string // ModeColor or ModeGray
	// Width and Height limit the scan area in pixels at Resolution; zero or
	// larger than the device maximum means the full area.
	Width, Height int
	Brightness    int // 0-100, default 50
	Contrast      int // 0-100, default 50
}

// Result describes a finished scan.
type Result struct {
	Pages int
	// Panel holds the options chosen on the device panel for a button scan,
	// or nil when the device has no button session.
	Panel *PanelSettings
	// Session holds K-command parameters from older devices.
	Session map[string]string
	// Resolution the device announced for the host settings.
	XDPI, YDPI int
	// DeviceSettings is true when the device scanned with its panel settings
	// (extended framing). ModeCode is the mode field of its data blocks.
	DeviceSettings bool
	ModeCode       uint16
}

// Client scans from one device.
type Client struct {
	Address string // host or host:port
	// ConnectTimeout bounds dialing and the handshake; PageTimeout bounds
	// waiting for scan data (feeder pick-up, platen warm-up, "next page?").
	ConnectTimeout time.Duration
	PageTimeout    time.Duration
	// ProbeTimeout bounds waiting for a GKP reply before falling back to K.
	ProbeTimeout time.Duration
	// Capture, when set, receives a copy of every byte read from the device.
	Capture io.Writer
}

// New returns a client for the device at address.
func New(address string) *Client {
	// The device waits on its panel for "next page?" between flatbed pages,
	// so the page timeout has to cover a person walking back to it.
	return &Client{Address: address, ConnectTimeout: 10 * time.Second, PageTimeout: 5 * time.Minute, ProbeTimeout: 5 * time.Second}
}

func (c *Client) addr() string {
	if _, _, err := net.SplitHostPort(c.Address); err == nil {
		return c.Address
	}
	return net.JoinHostPort(c.Address, strconv.Itoa(Port))
}

// Scan runs one scan and calls onPage with each page as it completes.
func (c *Client) Scan(ctx context.Context, s Settings, onPage func(page.Page) error) (Result, error) {
	conn, r, err := c.connect(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { conn.Close() }()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var res Result
	panel, v2, err := c.queryPanel(conn, r)
	if err != nil {
		return res, err
	}
	res.Panel = panel
	if !v2 {
		// Older device: reconnect and use the K command.
		conn.Close()
		if conn, r, err = c.connect(ctx); err != nil {
			return res, err
		}
		if res.Session, err = c.querySession(conn, r); err != nil {
			return res, err
		}
	}

	res, err = c.run(conn, r, s, res, onPage)
	if err != nil && ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, err
}

// connect dials and waits for "+OK", retrying while the device reports busy.
func (c *Client) connect(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	for attempt := 0; ; attempt++ {
		d := net.Dialer{Timeout: c.ConnectTimeout}
		conn, err := d.DialContext(ctx, "tcp", c.addr())
		if err != nil {
			return nil, nil, fmt.Errorf("brscan: connect %s: %w", c.addr(), err)
		}
		_ = conn.SetDeadline(time.Now().Add(c.ConnectTimeout))
		var src io.Reader = conn
		if c.Capture != nil {
			src = io.TeeReader(conn, c.Capture)
		}
		r := bufio.NewReader(src)
		line, err := r.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("brscan: read greeting: %w", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "+") {
			return conn, r, nil
		}
		conn.Close()
		if !strings.HasPrefix(line, "-NG") {
			return nil, nil, fmt.Errorf("brscan: unexpected greeting %q", line)
		}
		// The device stays busy while its panel is in use; give it a minute.
		if attempt >= 30 {
			return nil, nil, fmt.Errorf("%w (%s)", ErrBusy, line)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// queryPanel sends GKP. v2 is false when the device does not understand it.
func (c *Client) queryPanel(conn net.Conn, r *bufio.Reader) (panel *PanelSettings, v2 bool, err error) {
	if _, err := conn.Write([]byte{escape, 'G', 'K', 'P', '\n', msgEnd}); err != nil {
		return nil, false, fmt.Errorf("brscan: send GKP: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(c.ProbeTimeout))
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, false, nil //nolint:nilerr // no v2 reply; fall back to K
	}
	if string(hdr[1:4]) != "GKP" {
		return nil, false, nil
	}
	n := binary.LittleEndian.Uint32(hdr[4:])
	if n > 64<<10 {
		return nil, true, fmt.Errorf("brscan: implausible GKP length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, true, fmt.Errorf("brscan: read GKP reply: %w", err)
	}
	if hdr[0] != 0 {
		return nil, true, nil // e.g. 0x81: no button session
	}
	p := ParsePanelSettings(body)
	return &p, true, nil
}

// querySession sends the v1 K command.
func (c *Client) querySession(conn net.Conn, r *bufio.Reader) (map[string]string, error) {
	if _, err := conn.Write([]byte{escape, 'K', '\n', msgEnd}); err != nil {
		return nil, fmt.Errorf("brscan: send K: %w", err)
	}
	first, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("brscan: read session reply: %w", err)
	}
	switch first {
	case noParams:
		return nil, nil
	case withParams:
	default:
		return nil, fmt.Errorf("brscan: unexpected session reply 0x%02x", first)
	}
	rest, err := r.ReadBytes(msgEnd)
	if err != nil {
		return nil, fmt.Errorf("brscan: read session params: %w", err)
	}
	// Two unknown bytes follow 0x30; params end with '\n' 0x80.
	if len(rest) < 3 {
		return nil, fmt.Errorf("brscan: short session reply")
	}
	return parseParams(rest[2 : len(rest)-1]), nil
}

func (c *Client) run(conn net.Conn, r *bufio.Reader, s Settings, res Result, onPage func(page.Page) error) (Result, error) {
	mode := s.Mode
	if mode == "" {
		mode = ModeColor
	}
	brightness, contrast := s.Brightness, s.Contrast
	if brightness == 0 {
		brightness = 50
	}
	if contrast == 0 {
		contrast = 50
	}

	// Negotiate resolution and area.
	_ = conn.SetDeadline(time.Now().Add(c.ConnectTimeout))
	query := message('I', [][2]string{
		{"D", "SIN"},
		{"M", mode},
		{"R", fmt.Sprintf("%d,%d", s.Resolution, s.Resolution)},
	})
	if _, err := conn.Write(query); err != nil {
		return res, fmt.Errorf("brscan: send I: %w", err)
	}
	dims, err := readDimensions(r)
	if err != nil {
		return res, err
	}
	res.XDPI, res.YDPI = dims[0], dims[1]
	width, height := dims[4], dims[6]
	if s.Width > 0 && s.Width < width {
		width = s.Width
	}
	if s.Height > 0 && s.Height < height {
		height = s.Height
	}

	compression := "JPEG"
	if mode == ModeBW {
		compression = "RLENGTH" // JPEG cannot carry 1-bit data
	}
	start := message('X', [][2]string{
		{"A", fmt.Sprintf("0,0,%d,%d", width, height)},
		{"B", strconv.Itoa(brightness)},
		{"C", compression},
		{"D", "SIN"},
		{"G", "1"},
		{"L", "128"},
		{"M", mode},
		{"N", strconv.Itoa(contrast)},
		{"R", fmt.Sprintf("%d,%d", res.XDPI, res.YDPI)},
	})
	if _, err := conn.Write(start); err != nil {
		return res, fmt.Errorf("brscan: send X: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	rx := &receiver{r: r, res: &res, onPage: onPage, fallbackDPI: s.Resolution}
	if res.Panel != nil && res.Panel.XRes > 0 && res.Panel.YRes > 0 {
		rx.bilevelDPI = [2]int{int(res.Panel.XRes), int(res.Panel.YRes)}
	} else {
		rx.bilevelDPI = [2]int{res.XDPI, res.YDPI}
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(c.PageTimeout))
		done, err := rx.next()
		if err != nil || done {
			return res, err
		}
	}
}

// receiver assembles pages from the data stream.
type receiver struct {
	r           *bufio.Reader
	res         *Result
	onPage      func(page.Page) error
	fallbackDPI int
	bilevelDPI  [2]int

	jpeg    bytes.Buffer // JPEG data of the current page
	rows    []byte       // decoded bilevel rows of the current page
	stride  int          // bilevel bytes per row
	scratch []byte
	trace   []string
}

func (rx *receiver) note(s string) {
	rx.trace = append(rx.trace, s)
	if len(rx.trace) > 12 {
		rx.trace = rx.trace[1:]
	}
}

func (rx *receiver) fail(format string, args ...any) error {
	next, _ := rx.r.Peek(min(rx.r.Buffered(), 48))
	return fmt.Errorf("%s (page %d, recent blocks: %s; next bytes: % x)",
		fmt.Sprintf(format, args...), rx.res.Pages+1, strings.Join(rx.trace, " "), next)
}

// flush delivers the buffered page data, which may hold several JPEGs.
func (rx *receiver) flush() error {
	var pages []page.Page
	for _, img := range splitJPEGs(rx.jpeg.Bytes()) {
		p, err := page.NewJPEG(bytes.Clone(img))
		if err != nil {
			return fmt.Errorf("brscan: page %d: %w", rx.res.Pages+len(pages)+1, err)
		}
		if p.XDPI == 0 {
			p.XDPI, p.YDPI = rx.fallbackDPI, rx.fallbackDPI
		}
		pages = append(pages, p)
	}
	if len(rx.rows) > 0 && rx.stride > 0 {
		pages = append(pages, page.Page{
			Kind:   page.Bilevel,
			Data:   bytes.Clone(rx.rows[:len(rx.rows)/rx.stride*rx.stride]),
			Width:  rx.stride * 8,
			Height: len(rx.rows) / rx.stride,
			Stride: rx.stride,
			XDPI:   rx.bilevelDPI[0],
			YDPI:   rx.bilevelDPI[1],
		})
	}
	rx.jpeg.Reset()
	rx.rows = rx.rows[:0]
	rx.stride = 0
	for _, p := range pages {
		rx.res.Pages++
		if err := rx.onPage(p); err != nil {
			return err
		}
	}
	return nil
}

func (rx *receiver) finish() (bool, error) {
	if err := rx.flush(); err != nil {
		return true, err
	}
	if rx.res.Pages == 0 {
		return true, errors.New("brscan: scan ended without pages (is a document loaded?)")
	}
	return true, nil
}

// next processes one block; done is true at the end of the scan.
func (rx *receiver) next() (done bool, err error) {
	r := rx.r
	first, err := r.Peek(1)
	if err != nil {
		if errors.Is(err, io.EOF) && (rx.res.Pages > 0 || rx.jpeg.Len() > 0 || len(rx.rows) > 0) {
			return rx.finish() // device closed after the last page
		}
		return true, fmt.Errorf("brscan: waiting for scan data: %w", err)
	}
	if first[0] == msgEnd {
		_, _ = r.ReadByte()
		return rx.finish()
	}
	if first, err = r.Peek(2); err != nil {
		return true, rx.fail("brscan: read block header: %v", err)
	}

	if first[0] == 0x00 {
		return false, rx.extended(first[1])
	}

	var hdr [10]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return true, rx.fail("brscan: read block header: %v", err)
	}
	if hdr[1] != blockMagic || hdr[2] != 0 {
		return true, rx.fail("brscan: bad block header % x", hdr)
	}
	switch hdr[0] {
	case blockJPEG:
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return true, rx.fail("brscan: read block length: %v", err)
		}
		n := int(binary.LittleEndian.Uint16(lenBuf[:]))
		rx.note(fmt.Sprintf("[% x len=%d]", hdr, n))
		if _, err := io.CopyN(&rx.jpeg, r, int64(n)); err != nil {
			return true, rx.fail("brscan: read block data: %v", err)
		}
	case blockPageEnd:
		rx.note(fmt.Sprintf("[% x end]", hdr))
		return false, rx.flush()
	case blockRaw, blockPackBits:
		// One black & white row, raw or PackBits-compressed.
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return true, rx.fail("brscan: read block length: %v", err)
		}
		n := int(binary.LittleEndian.Uint16(lenBuf[:]))
		if err := rx.readRow(n, hdr[0] == blockPackBits); err != nil {
			return true, err
		}
	default:
		return true, rx.fail("brscan: unknown block type 0x%02x", hdr[0])
	}
	return false, nil
}

// extended handles the panel-settings framing.
func (rx *receiver) extended(kind byte) error {
	rx.res.DeviceSettings = true
	switch kind {
	case extData, extBilevel:
		var hdr [14]byte
		if _, err := io.ReadFull(rx.r, hdr[:]); err != nil {
			return rx.fail("brscan: read block header: %v", err)
		}
		rx.res.ModeCode = binary.LittleEndian.Uint16(hdr[4:6])
		n := binary.LittleEndian.Uint32(hdr[6:10])
		if n > 16<<20 {
			return rx.fail("brscan: implausible block length %d", n)
		}
		if kind == extData {
			rx.note(fmt.Sprintf("[% x len=%d]", hdr, n))
			if _, err := io.CopyN(&rx.jpeg, rx.r, int64(n)); err != nil {
				return rx.fail("brscan: read block data: %v", err)
			}
			return nil
		}
		return rx.readRow(int(n), true)
	case extPageEnd:
		var rec [4]byte
		if _, err := io.ReadFull(rx.r, rec[:]); err != nil {
			return rx.fail("brscan: read page end: %v", err)
		}
		rx.note(fmt.Sprintf("[% x end]", rec))
		return rx.flush()
	default:
		return rx.fail("brscan: unknown extended record 00 %02x", kind)
	}
}

// readRow reads one black & white row of n bytes, PackBits-compressed or raw.
// The first row of a page sets the stride.
func (rx *receiver) readRow(n int, packbits bool) error {
	if cap(rx.scratch) < n {
		rx.scratch = make([]byte, n)
	}
	row := rx.scratch[:n]
	if _, err := io.ReadFull(rx.r, row); err != nil {
		return rx.fail("brscan: read row: %v", err)
	}
	before := len(rx.rows)
	if packbits {
		var err error
		if rx.rows, err = page.UnpackBits(rx.rows, row); err != nil {
			return rx.fail("brscan: %v", err)
		}
	} else {
		rx.rows = append(rx.rows, row...)
	}
	if rx.stride == 0 {
		rx.stride = len(rx.rows) - before
	}
	return nil
}

// splitJPEGs splits concatenated JPEG files at EOI/SOI boundaries. Scanner
// JPEGs carry no embedded thumbnails, so FF D9 FF D8 only occurs between files.
func splitJPEGs(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	var out [][]byte
	boundary := []byte{0xff, 0xd9, 0xff, 0xd8}
	for {
		i := bytes.Index(b, boundary)
		if i < 0 {
			return append(out, b)
		}
		out = append(out, b[:i+2])
		b = b[i+2:]
	}
}

// message builds ESC <id> '\n' "K=V\n"... 0x80, skipping empty values.
func message(id byte, params [][2]string) []byte {
	b := []byte{escape, id, '\n'}
	for _, p := range params {
		if p[1] == "" {
			continue
		}
		b = append(b, p[0]...)
		b = append(b, '=')
		b = append(b, p[1]...)
		b = append(b, '\n')
	}
	return append(b, msgEnd)
}

func parseParams(b []byte) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// readDimensions reads the reply to ESC I.
func readDimensions(r *bufio.Reader) ([]int, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("brscan: read dimensions: %w", err)
	}
	if hdr[0] != 0 || hdr[2] != 0 {
		return nil, fmt.Errorf("brscan: bad dimensions header % x", hdr)
	}
	body := make([]byte, hdr[1])
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("brscan: read dimensions: %w", err)
	}
	return parseDimensions(body)
}

func parseDimensions(body []byte) ([]int, error) {
	var vals []int
	for f := range strings.SplitSeq(strings.TrimRight(string(body), "\x00"), ",") {
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("brscan: bad dimensions %q", body)
		}
		vals = append(vals, v)
	}
	if len(vals) < 7 {
		return nil, fmt.Errorf("brscan: bad dimensions %q", body)
	}
	return vals, nil
}
