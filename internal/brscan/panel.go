package brscan

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// PanelSettings are the scan options chosen on the device panel, from the
// GKP reply. Field meanings follow Brother's brscan5 parser
// (CParseCmdRes_v2::GetGKPcommandResponse); 0xff means "not set".
type PanelSettings struct {
	ScanKey, Timing byte // tag 0x1001

	XRes, YRes                 uint16 // tag 0x1002
	ColorType, PaperType       byte
	Direction                  byte
	Brightness, Contrast       byte
	Duplex, DuplexEdge         byte // tag 0x1003
	AutoCrop, Deskew, AutoRot  byte // tag 0x1004
	BlankRemoval, BleedThrough byte

	SaveFileType byte // tag 0x1005
	Compression  byte
	SplitType    byte // 0 one file, 1 one file per page, 4 every SplitPages pages
	SplitPages   byte

	Tags []uint16 // tags present, in order
}

// GKP tag IDs.
const (
	tagScanKey  = 0x1001
	tagScan     = 0x1002
	tagDuplex   = 0x1003
	tagImage    = 0x1004
	tagFileSave = 0x1005
	tagEnd      = 0xf0f0
)

// ParsePanelSettings decodes the GKP tag list.
func ParsePanelSettings(b []byte) PanelSettings {
	p := PanelSettings{SaveFileType: 0xff, ColorType: 0xff}
	for len(b) >= 4 {
		tag := binary.LittleEndian.Uint16(b)
		size := int(binary.LittleEndian.Uint16(b[2:]))
		b = b[4:]
		if size > len(b) {
			size = len(b)
		}
		v := b[:size]
		b = b[size:]
		p.Tags = append(p.Tags, tag)
		switch tag {
		case tagScanKey:
			if len(v) >= 2 {
				p.ScanKey, p.Timing = v[0], v[1]
			}
		case tagScan:
			if len(v) >= 9 {
				p.XRes = binary.LittleEndian.Uint16(v)
				p.YRes = binary.LittleEndian.Uint16(v[2:])
				p.ColorType, p.PaperType, p.Direction = v[4], v[5], v[6]
				p.Brightness, p.Contrast = v[7], v[8]
			}
		case tagDuplex:
			if len(v) >= 2 {
				p.Duplex, p.DuplexEdge = v[0], v[1]
			}
		case tagImage:
			if len(v) >= 5 {
				p.AutoCrop, p.Deskew, p.AutoRot = v[0], v[1], v[2]
				p.BlankRemoval, p.BleedThrough = v[3], v[4]
			}
		case tagFileSave:
			if len(v) >= 4 {
				p.SaveFileType, p.Compression, p.SplitType, p.SplitPages = v[0], v[1], v[2], v[3]
			}
		case tagEnd:
			return p
		}
	}
	return p
}

// FileType names the panel's file type. Codes map as in brscan5's
// GetFileSaveInfo onto its file type names.
func (p PanelSettings) FileType() string {
	switch p.SaveFileType {
	case 0x01:
		return "PDF"
	case 0x02:
		return "PDF/A"
	case 0x03:
		return "Secure PDF"
	case 0x04:
		return "Signed PDF"
	case 0x11:
		return "TIFF"
	case 0x12:
		return "TIFF Multi-Page"
	case 0x21:
		return "JPEG"
	case 0x22:
		return "PNG"
	case 0x23:
		return "BMP"
	case 0x24:
		return "XPS"
	case 0x41:
		return "TXT"
	case 0x42:
		return "RTF"
	case 0x43:
		return "HTML"
	case 0xff:
		return ""
	}
	return fmt.Sprintf("unknown(0x%02x)", p.SaveFileType)
}

func (p PanelSettings) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "file=%s(0x%02x) split=%d/%d res=%dx%d color=0x%02x paper=0x%02x duplex=%d",
		p.FileType(), p.SaveFileType, p.SplitType, p.SplitPages, p.XRes, p.YRes, p.ColorType, p.PaperType, p.Duplex)
	return b.String()
}
