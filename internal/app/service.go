// Package app wires registration, button events, scanning and storage together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brother"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/destination"
)

// Registration refresh timing, from NETserver's SKeyRegistInterval.plist.
const (
	refreshMin = 270 * time.Second
	refreshMax = 330 * time.Second
	retryMin   = 5 * time.Second
	retryMax   = 10 * time.Second
)

// RegState is the latest registration result for one target function.
type RegState struct {
	Target   string
	Function string
	Method   string
	LastOK   time.Time
	LastTry  time.Time
	Error    string
}

// Service is the running server.
type Service struct {
	log *slog.Logger

	mu          sync.Mutex
	cfg         *config.Config
	cfgErr      error
	hostIP      string
	registrar   brother.Registrar
	regs        map[string]*RegState
	jobs        []*Job
	nextJobID   int
	historyFile string
	running     bool
	kick        chan struct{}

	scanMu sync.Mutex // one scan at a time
}

// New creates a service. cfg may be nil when the config is invalid; cfgErr
// is then shown in the UI.
func New(log *slog.Logger, cfg *config.Config, cfgErr error) *Service {
	s := &Service{log: log, regs: map[string]*RegState{}, kick: make(chan struct{}, 1)}
	s.setConfig(cfg, cfgErr)
	return s
}

// Config returns the active config (may be nil) and the config error.
func (s *Service) Config() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.cfgErr
}

// Apply swaps in a new config and re-registers immediately.
func (s *Service) Apply(cfg *config.Config) {
	s.setConfig(cfg, nil)
	s.RegisterNow()
}

func (s *Service) setConfig(cfg *config.Config, cfgErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.cfgErr = cfgErr
	s.regs = map[string]*RegState{}
	s.registrar = nil
	if cfg != nil {
		s.registrar = newRegistrar(cfg.Printer)
	}
}

func newRegistrar(p config.Printer) brother.Registrar {
	phoenix := brother.NewPhoenix(p.Address, p.PhoenixUser, p.PhoenixPassword)
	snmp := &brother.SNMP{Address: p.Address, Community: p.SNMPCommunity}
	switch p.Registration {
	case "phoenix":
		return phoenix
	case "snmp":
		return snmp
	default:
		return &brother.Auto{Registrars: []brother.Registrar{phoenix, snmp}}
	}
}

// Run listens for button events and keeps registrations fresh until ctx ends.
func (s *Service) Run(ctx context.Context, eventPort int) error {
	errc := make(chan error, 1)
	go func() {
		errc <- brother.Listen(ctx, ":"+strconv.Itoa(eventPort), s.log, s.handleEvent)
	}()
	go s.registrationLoop(ctx)

	select {
	case err := <-errc:
		if err != nil {
			return fmt.Errorf("listen for button events on udp/%d: %w", eventPort, err)
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}

// RegisterNow triggers an immediate registration round.
func (s *Service) RegisterNow() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) registrationLoop(ctx context.Context) {
	for {
		wait := randDuration(refreshMin, refreshMax)
		if err := s.registerAll(ctx); err != nil {
			s.log.Warn("registration failed, retrying soon", "err", err)
			wait = randDuration(retryMin, retryMax)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.kick:
			t.Stop()
		case <-t.C:
		}
	}
}

func randDuration(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(rand.Int64N(int64(hi-lo)))
}

// registerAll registers every function of every target.
func (s *Service) registerAll(ctx context.Context) error {
	s.mu.Lock()
	cfg, registrar := s.cfg, s.registrar
	s.mu.Unlock()
	if cfg == nil {
		return nil // nothing configured yet; the UI shows the config error
	}

	hostIP, err := s.resolveHostIP(cfg)
	if err != nil {
		return err
	}

	var errs []error
	for _, t := range cfg.Targets {
		for _, fn := range brother.AllFunctions {
			if _, ok := t.Functions[fn.Lower()]; !ok {
				continue
			}
			reg := brother.Registration{
				HostIP:   hostIP,
				Port:     cfg.Server.EventPort,
				User:     t.Name,
				Function: fn,
				PIN:      t.PIN,
			}
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := registrar.Register(rctx, reg.Value())
			cancel()
			s.recordRegistration(t.Name, fn, registrar.Name(), err)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s/%s: %w", t.Name, fn.Lower(), err))
			}
		}
	}
	if len(errs) == 0 {
		s.log.Debug("registered with printer", "host", hostIP, "method", registrar.Name())
	}
	return errors.Join(errs...)
}

func (s *Service) recordRegistration(target string, fn brother.Function, method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := target + "/" + fn.Lower()
	st, ok := s.regs[key]
	if !ok {
		st = &RegState{Target: target, Function: fn.Lower()}
		s.regs[key] = st
	}
	st.LastTry = time.Now()
	st.Method = method
	if err != nil {
		st.Error = err.Error()
		return
	}
	st.Error = ""
	st.LastOK = st.LastTry
}

// resolveHostIP returns the configured advertise IP or the local address used
// to reach the printer.
func (s *Service) resolveHostIP(cfg *config.Config) (string, error) {
	ip := cfg.Server.AdvertiseIP
	if ip == "" {
		host := cfg.Printer.Address
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		conn, err := net.Dial("udp4", net.JoinHostPort(host, "161"))
		if err != nil {
			return "", fmt.Errorf("detect advertise IP (set server.advertise_ip): %w", err)
		}
		ip = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}
	s.mu.Lock()
	if s.hostIP != ip {
		s.log.Info("advertising to printer", "host", ip)
		s.hostIP = ip
	}
	s.mu.Unlock()
	return ip, nil
}

// Snapshot is a consistent view of the service state for the UI.
type Snapshot struct {
	Config        *config.Config
	ConfigError   error
	HostIP        string
	Registrations []RegState
	Jobs          []Job
	Scanning      bool
}

// Snapshot returns the current state.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{Config: s.cfg, ConfigError: s.cfgErr, HostIP: s.hostIP, Scanning: s.running}
	if s.cfg != nil {
		for _, t := range s.cfg.Targets {
			for _, fn := range brother.AllFunctions {
				if _, ok := t.Functions[fn.Lower()]; !ok {
					continue
				}
				if st, ok := s.regs[t.Name+"/"+fn.Lower()]; ok {
					snap.Registrations = append(snap.Registrations, *st)
				} else {
					snap.Registrations = append(snap.Registrations, RegState{Target: t.Name, Function: fn.Lower()})
				}
			}
		}
	}
	for i := len(s.jobs) - 1; i >= 0; i-- {
		j := *s.jobs[i]
		j.Files = append([]JobFile(nil), j.Files...)
		snap.Jobs = append(snap.Jobs, j)
	}
	return snap
}

func (s *Service) handleEvent(ev brother.Event, from *net.UDPAddr) {
	cfg, _ := s.Config()
	if cfg == nil {
		s.log.Warn("button pressed but server is not configured")
		return
	}
	target, ok := cfg.Target(ev.User)
	if !ok {
		if len(cfg.Targets) != 1 {
			s.log.Warn("button event for unknown target", "user", ev.User)
			return
		}
		target = &cfg.Targets[0]
	}
	fn := ev.Function
	if fn == "" {
		fn = brother.FuncFile
	}
	go func() {
		if _, err := s.Scan(context.Background(), ScanRequest{Target: target.Name, Function: fn.Lower(), Trigger: "button"}); err != nil {
			s.log.Error("scan failed", "target", target.Name, "function", fn.Lower(), "err", err)
		}
	}()
}

// ScanRequest describes a scan to run.
type ScanRequest struct {
	Target   string
	Function string // file, image, ocr or email
	Trigger  string // button, web or custom
	// Profile overrides the target's profile for the function (custom scans).
	// Empty fields take the function's defaults.
	Profile *config.Profile
}

// Scan runs one scan and stores the result.
func (s *Service) Scan(ctx context.Context, req ScanRequest) (*Job, error) {
	cfg, _ := s.Config()
	if cfg == nil {
		return nil, errors.New("server is not configured")
	}
	target, ok := cfg.Target(req.Target)
	if !ok {
		return nil, fmt.Errorf("unknown target %q", req.Target)
	}
	if req.Function == "" {
		req.Function = brother.FuncFile.Lower()
	}
	fn, err := brother.ParseFunction(req.Function)
	if err != nil {
		return nil, err
	}
	profile, offered := target.Functions[fn.Lower()]
	if req.Profile != nil {
		profile = config.MergeProfile(fn, *req.Profile)
		if err := profile.Validate(); err != nil {
			return nil, err
		}
	} else if !offered {
		return nil, fmt.Errorf("target %q does not offer %s", target.Name, fn.Lower())
	}

	job := s.startJob(target.Name, fn.Lower(), req.Trigger)
	log := s.log.With("job", job.ID, "target", target.Name, "function", fn.Lower())

	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.setRunning(true)
	defer s.setRunning(false)

	out, err := s.scanAndStore(ctx, cfg, target, fn, profile, req.Profile != nil, log)
	s.finishJob(job, out, err)
	if err != nil {
		return job, err
	}
	log.Info("scan stored", "pages", out.pages, "files", len(out.files))
	return job, nil
}

// TestDestination checks a target's destination.
func (s *Service) TestDestination(ctx context.Context, targetName string) error {
	cfg, _ := s.Config()
	if cfg == nil {
		return errors.New("server is not configured")
	}
	target, ok := cfg.Target(targetName)
	if !ok {
		return fmt.Errorf("unknown target %q", targetName)
	}
	dest, err := destination.New(target.Destination)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return dest.Test(ctx)
}

func (s *Service) setRunning(v bool) {
	s.mu.Lock()
	s.running = v
	s.mu.Unlock()
}
