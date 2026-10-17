// Package tiff writes multi-page TIFFs from scanned pages. JPEG pages are
// stored without re-encoding as one strip with TIFF JPEG compression (7)
// holding the complete JPEG stream; bilevel pages use PackBits.
package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/page"
)

// TIFF tags used here.
const (
	tagNewSubfileType   = 254
	tagImageWidth       = 256
	tagImageLength      = 257
	tagBitsPerSample    = 258
	tagCompression      = 259
	tagPhotometric      = 262
	tagStripOffsets     = 273
	tagSamplesPerPixel  = 277
	tagRowsPerStrip     = 278
	tagStripByteCounts  = 279
	tagXResolution      = 282
	tagYResolution      = 283
	tagPlanarConfig     = 284
	tagResolutionUnit   = 296
	tagPageNumber       = 297
	tagYCbCrSubSampling = 530
)

// Field types.
const (
	typeShort    = 3
	typeLong     = 4
	typeRational = 5
)

// Compression and photometric values.
const (
	compressionJPEG     = 7
	compressionPackBits = 32773

	photometricWhiteIsZero = 0
	photometricBlackIsZero = 1
	photometricYCbCr       = 6
)

// Info describes a JPEG frame.
type Info struct {
	Width, Height int
	Components    int
	// HSamp, VSamp are the luma sampling factors (YCbCr subsampling).
	HSamp, VSamp int
}

// ParseJPEG reads the frame header of a baseline or progressive JPEG.
func ParseJPEG(data []byte) (Info, error) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return Info{}, errors.New("tiff: not a JPEG")
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xff {
			return Info{}, errors.New("tiff: corrupt JPEG marker")
		}
		marker := data[i+1]
		if marker == 0xff {
			i++
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:]))
		switch marker {
		case 0xc0, 0xc1, 0xc2:
			s := data[i+4:]
			if len(s) < 6 {
				return Info{}, errors.New("tiff: short frame header")
			}
			info := Info{
				Height:     int(binary.BigEndian.Uint16(s[1:])),
				Width:      int(binary.BigEndian.Uint16(s[3:])),
				Components: int(s[5]),
				HSamp:      1,
				VSamp:      1,
			}
			if info.Components >= 1 && len(s) >= 9 {
				info.HSamp = int(s[7] >> 4)
				info.VSamp = int(s[7] & 0x0f)
			}
			return info, nil
		case 0xd9, 0xda:
			return Info{}, errors.New("tiff: JPEG without frame header")
		}
		i += 2 + segLen
	}
	return Info{}, errors.New("tiff: JPEG without frame header")
}

type entry struct {
	tag, typ uint16
	count    uint32
	value    []byte // little-endian encoded values
}

func shorts(v ...uint16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return b
}

func longs(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

// pageEntries returns the strip data and format-specific tags for a page.
func pageEntries(p page.Page) (strip []byte, width, height int, tags []entry, err error) {
	switch p.Kind {
	case page.JPEG:
		info, err := ParseJPEG(p.Data)
		if err != nil {
			return nil, 0, 0, nil, err
		}
		switch info.Components {
		case 3:
			tags = []entry{
				{tagBitsPerSample, typeShort, 3, shorts(8, 8, 8)},
				{tagCompression, typeShort, 1, shorts(compressionJPEG)},
				{tagPhotometric, typeShort, 1, shorts(photometricYCbCr)},
				{tagSamplesPerPixel, typeShort, 1, shorts(3)},
				{tagYCbCrSubSampling, typeShort, 2, shorts(uint16(info.HSamp), uint16(info.VSamp))},
			}
		case 1:
			tags = []entry{
				{tagBitsPerSample, typeShort, 1, shorts(8)},
				{tagCompression, typeShort, 1, shorts(compressionJPEG)},
				{tagPhotometric, typeShort, 1, shorts(photometricBlackIsZero)},
				{tagSamplesPerPixel, typeShort, 1, shorts(1)},
			}
		default:
			return nil, 0, 0, nil, fmt.Errorf("unsupported JPEG with %d components", info.Components)
		}
		return p.Data, info.Width, info.Height, tags, nil
	case page.Bilevel:
		rowBytes := (p.Width + 7) / 8
		var strip []byte
		for y := range p.Height {
			strip = page.PackBits(strip, p.Data[y*p.Stride:y*p.Stride+rowBytes])
		}
		tags = []entry{
			{tagBitsPerSample, typeShort, 1, shorts(1)},
			{tagCompression, typeShort, 1, shorts(compressionPackBits)},
			{tagPhotometric, typeShort, 1, shorts(photometricWhiteIsZero)}, // 1 = black
			{tagSamplesPerPixel, typeShort, 1, shorts(1)},
		}
		return strip, p.Width, p.Height, tags, nil
	}
	return nil, 0, 0, nil, fmt.Errorf("unsupported page kind %d", p.Kind)
}

// Write writes all pages as one TIFF. fallbackDPI is used for pages whose
// resolution is unknown.
func Write(w io.Writer, pages []page.Page, fallbackDPI int) error {
	if len(pages) == 0 {
		return errors.New("tiff: no pages")
	}
	var buf bytes.Buffer
	buf.Write([]byte{'I', 'I', 42, 0, 0, 0, 0, 0}) // header; first IFD offset patched below
	prevNext := 4                                  // where to patch the next-IFD offset

	for n, p := range pages {
		strip, width, height, entries, err := pageEntries(p)
		if err != nil {
			return fmt.Errorf("tiff: page %d: %w", n+1, err)
		}
		xdpi, ydpi := p.DPI(fallbackDPI)

		// Image data, word aligned.
		if buf.Len()%2 == 1 {
			buf.WriteByte(0)
		}
		dataOff := buf.Len()
		buf.Write(strip)
		if buf.Len()%2 == 1 {
			buf.WriteByte(0)
		}

		entries = append(entries,
			entry{tagNewSubfileType, typeLong, 1, longs(2)}, // page of a multi-page document
			entry{tagImageWidth, typeLong, 1, longs(uint32(width))},
			entry{tagImageLength, typeLong, 1, longs(uint32(height))},
			entry{tagStripOffsets, typeLong, 1, longs(uint32(dataOff))},
			entry{tagRowsPerStrip, typeLong, 1, longs(uint32(height))},
			entry{tagStripByteCounts, typeLong, 1, longs(uint32(len(strip)))},
			entry{tagXResolution, typeRational, 1, longs(uint32(xdpi), 1)},
			entry{tagYResolution, typeRational, 1, longs(uint32(ydpi), 1)},
			entry{tagPlanarConfig, typeShort, 1, shorts(1)},
			entry{tagResolutionUnit, typeShort, 1, shorts(2)}, // inch
			entry{tagPageNumber, typeShort, 2, shorts(uint16(n), uint16(len(pages)))},
		)
		sort.Slice(entries, func(i, j int) bool { return entries[i].tag < entries[j].tag })

		// Out-of-line values first, then the IFD itself.
		extOff := map[int]int{}
		for i, e := range entries {
			if len(e.value) > 4 {
				if buf.Len()%2 == 1 {
					buf.WriteByte(0)
				}
				extOff[i] = buf.Len()
				buf.Write(e.value)
			}
		}
		if buf.Len()%2 == 1 {
			buf.WriteByte(0)
		}
		ifdOff := buf.Len()
		binary.LittleEndian.PutUint32(buf.Bytes()[prevNext:], uint32(ifdOff))

		_ = binary.Write(&buf, binary.LittleEndian, uint16(len(entries)))
		for i, e := range entries {
			_ = binary.Write(&buf, binary.LittleEndian, e.tag)
			_ = binary.Write(&buf, binary.LittleEndian, e.typ)
			_ = binary.Write(&buf, binary.LittleEndian, e.count)
			if off, ok := extOff[i]; ok {
				_ = binary.Write(&buf, binary.LittleEndian, uint32(off))
			} else {
				v := make([]byte, 4)
				copy(v, e.value)
				buf.Write(v)
			}
		}
		prevNext = buf.Len()
		buf.Write([]byte{0, 0, 0, 0}) // next IFD, patched by the following page
	}
	if uint64(buf.Len()) > math.MaxUint32 { // TIFF offsets are 32-bit

		return errors.New("tiff: file too large")
	}
	_, err := w.Write(buf.Bytes())
	return err
}
