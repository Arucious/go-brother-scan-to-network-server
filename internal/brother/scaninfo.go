// Package brother implements the Brother "scan key" (push scan) protocol used
// by the scanner's "Scan to PC" button: registering a host with the device and
// receiving button events.
//
// The protocol details were recovered from Brother's macOS NETserver
// (BrotherPushScanTool 2.3.1):
//
//   - The host registers itself by setting the string MIB value
//     1.3.6.1.4.1.2435.2.3.9.2.11.1.1.0 to a "KEY=VALUE;" list, either over the
//     device's HTTPS "Phoenix" endpoint (/phoenix/mib) or over SNMP.
//   - A registration lives for DURATION seconds (360) and is refreshed every
//     270-330 seconds. One registration is sent per panel function.
//   - When the button is pressed the device sends a UDP datagram to the
//     registered HOST port (54925). The datagram is a 4 byte header followed by
//     the same "KEY=VALUE;" encoding; the host acknowledges it by echoing the
//     datagram back.
package brother

import (
	"fmt"
	"strconv"
	"strings"
)

// ScanKeyOID is the MIB value used to register scan-key destinations.
const ScanKeyOID = "1.3.6.1.4.1.2435.2.3.9.2.11.1.1.0"

// DefaultEventPort is the UDP port the device sends button events to.
const DefaultEventPort = 54925

// RegistrationDuration is how long (seconds) a registration stays valid.
const RegistrationDuration = 360

// Function is a scan-to-PC panel function.
type Function string

const (
	FuncImage Function = "IMAGE"
	FuncEmail Function = "EMAIL"
	FuncOCR   Function = "OCR"
	FuncFile  Function = "FILE"
)

// AllFunctions lists the functions NETserver registers, in its order.
var AllFunctions = []Function{FuncImage, FuncEmail, FuncOCR, FuncFile}

// appNum mirrors NETserver's FUNC -> APPNUM table. FILE is registered with
// function index 5, so it carries APPNUM 5.
var appNum = map[Function]int{
	FuncImage: 1,
	FuncEmail: 2,
	FuncOCR:   3,
	FuncFile:  5,
}

// ParseFunction parses a function name case-insensitively.
func ParseFunction(s string) (Function, error) {
	f := Function(strings.ToUpper(strings.TrimSpace(s)))
	if _, ok := appNum[f]; !ok {
		return "", fmt.Errorf("unknown scan function %q (want image, email, ocr or file)", s)
	}
	return f, nil
}

// Lower returns the lowercase function name used in configuration.
func (f Function) Lower() string { return strings.ToLower(string(f)) }

// Pair is a single KEY=VALUE entry.
type Pair struct {
	Key   string
	Value string
}

// ScanInfo is an ordered list of KEY=VALUE entries.
type ScanInfo []Pair

// Encode renders the entries as "KEY=VALUE;KEY=VALUE;".
func (s ScanInfo) Encode() string {
	var b strings.Builder
	for _, p := range s {
		b.WriteString(p.Key)
		b.WriteByte('=')
		b.WriteString(p.Value)
		b.WriteByte(';')
	}
	return b.String()
}

// Get returns the value for key (case-sensitive).
func (s ScanInfo) Get(key string) (string, bool) {
	for _, p := range s {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// ParseScanInfo decodes "KEY=VALUE;" entries, stopping at a NUL byte.
func ParseScanInfo(b []byte) ScanInfo {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	var out ScanInfo
	for _, part := range strings.Split(string(b), ";") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		out = append(out, Pair{Key: strings.TrimSpace(k), Value: v})
	}
	return out
}

// Registration describes one scan-key registration.
type Registration struct {
	// HostIP is the address the device sends events to.
	HostIP string
	// Port is the UDP event port. NETserver always uses 54925.
	Port int
	// User is the name shown on the device panel.
	User string
	// Function is the panel function being registered.
	Function Function
	// PIN is an optional numeric PIN (sent as BRID).
	PIN string
}

// Value renders the MIB value in the same field order as NETserver.
func (r Registration) Value() string {
	port := r.Port
	if port == 0 {
		port = DefaultEventPort
	}
	info := ScanInfo{
		{"TYPE", "BR"},
		{"BUTTON", "SCAN"},
		{"DURATION", strconv.Itoa(RegistrationDuration)},
		{"CC", "1"},
		{"HOST", fmt.Sprintf("%s:%d", r.HostIP, port)},
		{"USER", `"` + r.User + `"`},
	}
	if r.PIN != "" {
		info = append(info, Pair{"BRID", r.PIN})
	}
	info = append(info,
		Pair{"FUNC", string(r.Function)},
		Pair{"APPNUM", strconv.Itoa(appNum[r.Function])},
	)
	return info.Encode()
}

// Event is a decoded button event.
type Event struct {
	Info     ScanInfo
	User     string
	Function Function
}

// Header bytes NETserver accepts for button events.
const (
	eventPacketType    = 0x02
	eventPacketSubType = 0x30
)

// ParseEventPacket decodes a UDP button event. It returns ok=false for
// datagrams that are not scan-key events; those must not be acknowledged.
func ParseEventPacket(b []byte) (ev Event, ok bool, err error) {
	if len(b) < 4 {
		return Event{}, false, fmt.Errorf("packet too short (%d bytes)", len(b))
	}
	if b[0] != eventPacketType || b[3] != eventPacketSubType {
		return Event{}, false, nil
	}
	info := ParseScanInfo(b[4:])
	ev = Event{Info: info}
	if u, found := info.Get("USER"); found {
		ev.User = strings.Trim(u, `"`)
	}
	if f, found := info.Get("FUNC"); found {
		fn, perr := ParseFunction(f)
		if perr != nil {
			return ev, true, perr
		}
		ev.Function = fn
	}
	return ev, true, nil
}
