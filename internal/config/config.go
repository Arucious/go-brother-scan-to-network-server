// Package config loads and validates the server configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	brotherscan "github.com/aurimasniekis/go-brother-scan-to-network-server"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brother"
)

// Example is the example configuration (config.yaml.example).
var Example = brotherscan.ExampleConfig

// Config is the root configuration.
type Config struct {
	Printer Printer  `yaml:"printer"`
	Server  Server   `yaml:"server"`
	Web     Web      `yaml:"web"`
	Targets []Target `yaml:"targets"`
}

// Printer configures how to reach the device.
type Printer struct {
	// Address is the printer IP or hostname.
	Address string `yaml:"address"`
	// Registration selects how to register: auto, phoenix or snmp.
	Registration    string `yaml:"registration"`
	PhoenixUser     string `yaml:"phoenix_user"`
	PhoenixPassword string `yaml:"phoenix_password"`
	SNMPCommunity   string `yaml:"snmp_community"`
	// ScanProtocol selects how pages are pulled: native (Brother TCP 54921,
	// required for button scans) or escl.
	ScanProtocol string `yaml:"scan_protocol"`
	// ESCLAddress overrides host[:port] used for eSCL; defaults to Address.
	ESCLAddress string `yaml:"escl_address"`
}

// Server configures the button event listener.
type Server struct {
	// AdvertiseIP is the address the printer sends events to. Detected
	// automatically when empty.
	AdvertiseIP string `yaml:"advertise_ip"`
	// EventPort is the UDP port to listen on and advertise.
	EventPort int `yaml:"event_port"`
}

// Web configures the web UI.
type Web struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Target is one entry in the printer's "Scan to PC" list.
type Target struct {
	// Name is shown on the printer panel.
	Name string `yaml:"name"`
	// PIN optionally protects the target on the panel.
	PIN string `yaml:"pin"`
	// Filename is a Go template for the output path, without extension.
	Filename string `yaml:"filename"`
	// Functions maps panel functions (file, image, ocr, email) to profiles.
	// Only listed functions are registered; empty registers all four.
	Functions   map[string]Profile `yaml:"functions"`
	Destination Destination        `yaml:"destination"`
}

// Profile controls how a function scans.
type Profile struct {
	Format     string `yaml:"format"`     // see Formats
	Color      string `yaml:"color"`      // color, gray or bw (black & white)
	Resolution int    `yaml:"resolution"` // 100, 200, 300 or 600
	Source     string `yaml:"source"`     // auto, platen or adf
	PageSize   string `yaml:"page_size"`  // a4, letter, legal or max
}

// Destination is where scans are stored.
type Destination struct {
	Type string `yaml:"type"` // local, smb, ftp or s3

	// Path is the local directory (local), the directory inside the share or
	// FTP home (smb, ftp), or the object key prefix (s3).
	Path string `yaml:"path"`

	// smb and ftp
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// smb
	Share  string `yaml:"share"`
	Domain string `yaml:"domain"`

	// ftp: TLS is "" (plain FTP), "explicit" (FTPS via AUTH TLS) or
	// "implicit" (FTPS on port 990).
	TLS string `yaml:"tls"`

	// ftp and s3: accept self-signed TLS certificates.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`

	// s3: Endpoint is empty for AWS, or host[:port] / URL of an S3-compatible
	// service (an http:// URL disables TLS). PathStyle is needed by MinIO and
	// most self-hosted services.
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	SessionToken    string `yaml:"session_token"`
	PathStyle       bool   `yaml:"path_style"`
	StorageClass    string `yaml:"storage_class"`
}

// DestinationTypes are the supported destination types.
var DestinationTypes = []string{"local", "smb", "ftp", "s3"}

// DefaultFilename is used when a target has no filename template.
const DefaultFilename = `{{.Time.Format "2006-01-02_15-04-05"}}_{{.Function}}`

var functionDefaults = map[brother.Function]Profile{
	brother.FuncFile:  {Format: "pdf", Color: "gray", Resolution: 300},
	brother.FuncOCR:   {Format: "pdf", Color: "gray", Resolution: 300},
	brother.FuncEmail: {Format: "pdf", Color: "color", Resolution: 200},
	brother.FuncImage: {Format: "jpeg", Color: "color", Resolution: 300},
}

// Load reads, expands, defaults and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses config YAML. ${VAR} references are expanded from the environment.
func Parse(data []byte) (*Config, error) {
	expanded := os.Expand(string(data), func(key string) string {
		if key == "$" {
			return "$"
		}
		return os.Getenv(key)
	})
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// WriteExample writes the example config to path, creating parent dirs.
func WriteExample(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, Example, 0o600)
}

func (c *Config) applyDefaults() {
	if c.Printer.Registration == "" {
		c.Printer.Registration = "auto"
	}
	if c.Printer.PhoenixUser == "" {
		c.Printer.PhoenixUser = "Public"
	}
	if c.Printer.PhoenixPassword == "" {
		c.Printer.PhoenixPassword = "0000"
	}
	if c.Printer.SNMPCommunity == "" {
		c.Printer.SNMPCommunity = "internal"
	}
	if c.Printer.ScanProtocol == "" {
		c.Printer.ScanProtocol = "native"
	}
	if c.Server.EventPort == 0 {
		c.Server.EventPort = brother.DefaultEventPort
	}
	if c.Web.Listen == "" {
		c.Web.Listen = ":8080"
	}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Filename == "" {
			t.Filename = DefaultFilename
		}
		if len(t.Functions) == 0 {
			t.Functions = map[string]Profile{}
			for _, f := range brother.AllFunctions {
				t.Functions[f.Lower()] = Profile{}
			}
		}
		for name, p := range t.Functions {
			fn, err := brother.ParseFunction(name)
			if err != nil {
				continue // reported by Validate
			}
			t.Functions[name] = MergeProfile(fn, p)
		}
		d := &t.Destination
		if d.Port == 0 {
			switch {
			case d.Type == "smb":
				d.Port = 445
			case d.Type == "ftp" && d.TLS == "implicit":
				d.Port = 990
			case d.Type == "ftp":
				d.Port = 21
			}
		}
		if d.Username == "" {
			switch d.Type {
			case "ftp":
				d.Username = "anonymous"
			case "smb":
				d.Username = "guest" // go-smb2 has no anonymous login
			}
		}
	}
}

// MergeProfile fills the empty fields of p with the function's defaults.
func MergeProfile(fn brother.Function, p Profile) Profile {
	d := functionDefaults[fn]
	if p.Format == "" {
		p.Format = d.Format
	}
	if p.Color == "" {
		p.Color = d.Color
	}
	if p.Resolution == 0 {
		p.Resolution = d.Resolution
	}
	if p.Source == "" {
		p.Source = "auto"
	}
	if p.PageSize == "" {
		p.PageSize = "a4"
	}
	return p
}

// Validate reports configuration errors.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Printer.Address == "" {
		add("printer.address is required")
	}
	switch c.Printer.Registration {
	case "auto", "phoenix", "snmp":
	default:
		add("printer.registration must be auto, phoenix or snmp")
	}
	switch c.Printer.ScanProtocol {
	case "native", "escl":
	default:
		add("printer.scan_protocol must be native or escl")
	}
	if len(c.Targets) == 0 {
		add("at least one target is required")
	}

	seen := map[string]bool{}
	for i, t := range c.Targets {
		where := fmt.Sprintf("targets[%d]", i)
		if t.Name == "" {
			add("%s.name is required", where)
		} else {
			where = fmt.Sprintf("target %q", t.Name)
			if len(t.Name) > 15 {
				add("%s: name must be at most 15 characters (printer panel limit)", where)
			}
			if strings.ContainsAny(t.Name, `";=`) {
				add(`%s: name must not contain '"', ';' or '='`, where)
			}
			if seen[strings.ToLower(t.Name)] {
				add("%s: duplicate target name", where)
			}
			seen[strings.ToLower(t.Name)] = true
		}
		if t.PIN != "" && strings.Trim(t.PIN, "0123456789") != "" {
			add("%s: pin must be numeric", where)
		}
		if _, err := template.New("filename").Parse(t.Filename); err != nil {
			add("%s: invalid filename template: %v", where, err)
		}
		for name, p := range t.Functions {
			if _, err := brother.ParseFunction(name); err != nil {
				add("%s: %v", where, err)
				continue
			}
			if err := p.Validate(); err != nil {
				add("%s function %s: %v", where, name, err)
			}
		}
		d := t.Destination
		switch d.Type {
		case "local":
			if d.Path == "" {
				add("%s: destination.path is required for local destinations", where)
			}
		case "smb":
			if d.Host == "" || d.Share == "" {
				add("%s: destination.host and destination.share are required for smb", where)
			}
		case "ftp":
			if d.Host == "" {
				add("%s: destination.host is required for ftp", where)
			}
			if d.TLS != "" && d.TLS != "explicit" && d.TLS != "implicit" {
				add("%s: destination.tls must be empty, explicit or implicit", where)
			}
		case "s3":
			if d.Bucket == "" {
				add("%s: destination.bucket is required for s3", where)
			}
			if (d.AccessKeyID == "") != (d.SecretAccessKey == "") {
				add("%s: destination.access_key_id and secret_access_key must be set together", where)
			}
		default:
			add("%s: destination.type must be one of %s", where, strings.Join(DestinationTypes, ", "))
		}
	}
	return errors.Join(errs...)
}

// Option values accepted in profiles, in display order.
var (
	Colors      = []string{"color", "gray", "bw"}
	Resolutions = []int{100, 200, 300, 600}
	Sources     = []string{"auto", "platen", "adf"}
	PageSizeIDs = []string{"a4", "letter", "legal", "max"}
)

// Validate checks a fully defaulted profile.
func (p Profile) Validate() error {
	var errs []error
	if !slices.Contains(Formats, p.Format) {
		errs = append(errs, fmt.Errorf("format must be one of %s", strings.Join(Formats, ", ")))
	}
	if !slices.Contains(Colors, p.Color) {
		errs = append(errs, errors.New("color must be color, gray or bw"))
	}
	if !slices.Contains(Resolutions, p.Resolution) {
		errs = append(errs, errors.New("resolution must be 100, 200, 300 or 600"))
	}
	if !slices.Contains(Sources, p.Source) {
		errs = append(errs, errors.New("source must be auto, platen or adf"))
	}
	if !slices.Contains(PageSizeIDs, p.PageSize) {
		errs = append(errs, errors.New("page_size must be a4, letter, legal or max"))
	}
	return errors.Join(errs...)
}

// Formats are the supported output formats. "-single" variants write one file
// per page; jpeg always does.
var Formats = []string{"pdf", "pdf-single", "tiff", "tiff-single", "jpeg", "png", "bmp"}

// PageSizes in 1/300 inch (width, height).
var PageSizes = map[string][2]int{
	"a4":     {2480, 3507},
	"letter": {2550, 3300},
	"legal":  {2550, 4200},
}

// Target returns the target with the given name (case-insensitive).
func (c *Config) Target(name string) (*Target, bool) {
	for i := range c.Targets {
		if strings.EqualFold(c.Targets[i].Name, name) {
			return &c.Targets[i], true
		}
	}
	return nil, false
}

// FilenameData is available to filename templates.
type FilenameData struct {
	Time     time.Time
	Target   string
	Function string
}

// RenderFilename renders the target's filename template.
func (t *Target) RenderFilename(data FilenameData) (string, error) {
	tmpl, err := template.New("filename").Option("missingkey=error").Parse(t.Filename)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return CleanRelPath(b.String())
}

// CleanRelPath normalises a relative output path and rejects escapes.
func CleanRelPath(p string) (string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	var parts []string
	for _, part := range strings.Split(p, "/") {
		part = strings.TrimSpace(part)
		switch part {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("path %q must not contain '..'", p)
		}
		parts = append(parts, strings.Map(func(r rune) rune {
			if strings.ContainsRune(`:*?"<>|`, r) || r < 32 {
				return '_'
			}
			return r
		}, part))
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("filename template rendered an empty path")
	}
	return strings.Join(parts, "/"), nil
}
