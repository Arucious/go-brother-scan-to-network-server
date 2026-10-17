package brother

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Registrar sets the scan-key MIB value on the device.
type Registrar interface {
	Register(ctx context.Context, value string) error
	Name() string
}

// Phoenix is the HTTPS MIB endpoint newer Brother devices expose. SNMP writes
// are usually disabled on these devices, so this is the primary method.
//
// NETserver authenticates with the fixed credentials Public/0000 using HTTP
// Digest auth and POSTs {"request":[{"key":OID,"string_value":V}]} to
// https://<device>/phoenix/mib. The device answers
// {"response":[{"key":OID,"status":0}]}, where a non-zero status is an error.
type Phoenix struct {
	Address  string
	User     string
	Password string
	Client   *http.Client
}

// NewPhoenix returns a Phoenix registrar for the device address.
func NewPhoenix(address, user, password string) *Phoenix {
	return &Phoenix{
		Address:  address,
		User:     user,
		Password: password,
		Client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				// Brother devices ship a self-signed certificate.
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				DisableKeepAlives: true,
			},
		},
	}
}

func (p *Phoenix) Name() string { return "phoenix" }

type phoenixItem struct {
	Key         string  `json:"key"`
	StringValue *string `json:"string_value,omitempty"`
}

type phoenixRequest struct {
	Request []phoenixItem `json:"request"`
}

type phoenixResponse struct {
	Response []struct {
		Key    string `json:"key"`
		Status int    `json:"status"`
	} `json:"response"`
}

// Register sets the scan-key value.
func (p *Phoenix) Register(ctx context.Context, value string) error {
	body, err := json.Marshal(phoenixRequest{Request: []phoenixItem{{Key: ScanKeyOID, StringValue: &value}}})
	if err != nil {
		return err
	}
	resp, err := p.post(ctx, "/phoenix/mib", body)
	if err != nil {
		return err
	}
	var pr phoenixResponse
	if err := json.Unmarshal(resp, &pr); err != nil {
		return fmt.Errorf("phoenix: decode response %q: %w", resp, err)
	}
	if len(pr.Response) == 0 {
		return fmt.Errorf("phoenix: empty response")
	}
	for _, r := range pr.Response {
		if r.Status != 0 {
			return fmt.Errorf("phoenix: device rejected %s (status %d)", r.Key, r.Status)
		}
	}
	return nil
}

func (p *Phoenix) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	url := "https://" + p.Address + path
	do := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return p.Client.Do(req)
	}

	resp, err := do("")
	if err != nil {
		return nil, fmt.Errorf("phoenix: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		c, err := parseDigestChallenge(challenge)
		if err != nil {
			return nil, fmt.Errorf("phoenix: %w", err)
		}
		resp, err = do(c.authorization(http.MethodPost, path, p.User, p.Password))
		if err != nil {
			return nil, fmt.Errorf("phoenix: %w", err)
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("phoenix: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("phoenix: authentication failed (check phoenix_user/phoenix_password)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("phoenix: unexpected HTTP status %s", resp.Status)
	}
	return data, nil
}

// SNMP registers over SNMP, as older Brother devices and Linux brscan-skey do.
type SNMP struct {
	Address   string
	Community string
	Timeout   time.Duration
}

func (s *SNMP) Name() string { return "snmp" }

// Register sets the scan-key value with an SNMPv1 set request.
func (s *SNMP) Register(ctx context.Context, value string) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	g := &gosnmp.GoSNMP{
		Target:    s.Address,
		Port:      161,
		Community: s.Community,
		Version:   gosnmp.Version1,
		Timeout:   timeout,
		Retries:   1,
		Context:   ctx,
	}
	if host, port, err := net.SplitHostPort(s.Address); err == nil {
		g.Target = host
		var p uint16
		if _, err := fmt.Sscanf(port, "%d", &p); err == nil {
			g.Port = p
		}
	}
	if err := g.Connect(); err != nil {
		return fmt.Errorf("snmp: %w", err)
	}
	defer g.Conn.Close()
	res, err := g.Set([]gosnmp.SnmpPDU{{Name: ScanKeyOID, Type: gosnmp.OctetString, Value: []byte(value)}})
	if err != nil {
		return fmt.Errorf("snmp: %w", err)
	}
	if res.Error != gosnmp.NoError {
		return fmt.Errorf("snmp: device rejected set: %s", res.Error)
	}
	return nil
}

// Auto tries each registrar in order and sticks with the first that works.
type Auto struct {
	Registrars []Registrar

	mu      sync.Mutex
	working Registrar
}

func (a *Auto) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.working != nil {
		return "auto/" + a.working.Name()
	}
	return "auto"
}

// Register uses the last working registrar first, then falls back to the rest.
func (a *Auto) Register(ctx context.Context, value string) error {
	a.mu.Lock()
	working := a.working
	a.mu.Unlock()

	if working != nil {
		if err := working.Register(ctx, value); err == nil {
			return nil
		}
	}
	var errs []error
	for _, r := range a.Registrars {
		if r == working {
			continue
		}
		err := r.Register(ctx, value)
		if err == nil {
			a.mu.Lock()
			a.working = r
			a.mu.Unlock()
			return nil
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
