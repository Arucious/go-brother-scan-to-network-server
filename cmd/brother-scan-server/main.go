// Command brother-scan-server receives Brother "Scan to PC" button presses and
// stores the scans on an SMB share or local directory.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo; lets TZ work in filenames

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/app"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brother"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/config"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", envOr("CONFIG_PATH", "config.yaml"), "path to the YAML config file (env CONFIG_PATH)")
	logLevel := flag.String("log-level", envOr("LOG_LEVEL", "info"), "log level: debug, info, warn, error (env LOG_LEVEL)")
	showVersion := flag.Bool("version", false, "print version and exit")
	healthcheck := flag.Bool("healthcheck", false, "check that the local web UI responds, then exit (Docker HEALTHCHECK)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *healthcheck {
		return checkHealth(*configPath)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(*logLevel))); err != nil {
		return fmt.Errorf("invalid log level %q", *logLevel)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("starting brother-scan-server", "version", version, "config", *configPath)

	if _, err := os.Stat(*configPath); errors.Is(err, os.ErrNotExist) {
		if err := config.WriteExample(*configPath); err != nil {
			return fmt.Errorf("write example config: %w", err)
		}
		log.Warn("no config found, wrote an example; edit it in the web UI", "path", *configPath)
	}
	cfg, cfgErr := config.Load(*configPath)
	if cfgErr != nil {
		log.Error("configuration is invalid; fix it in the web UI", "err", cfgErr)
		cfg = nil
	}

	eventPort := brother.DefaultEventPort
	listen := ":8080"
	if cfg != nil {
		eventPort = cfg.Server.EventPort
		listen = cfg.Web.Listen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := app.New(log, cfg, cfgErr)
	historyFile := filepath.Join(filepath.Dir(*configPath), "jobs.json")
	if err := svc.SetHistoryFile(historyFile); err != nil {
		log.Warn("could not load job history", "file", historyFile, "err", err)
	}
	ui := web.New(svc, *configPath, log)
	httpServer := &http.Server{Addr: listen, Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() {
		log.Info("web UI listening", "addr", listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("web UI: %w", err)
		}
	}()
	go func() {
		log.Info("listening for scan button events", "port", eventPort)
		if err := svc.Run(ctx, eventPort); err != nil {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		stop()
		return err
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// checkHealth requests /healthz from the web UI configured in configPath.
func checkHealth(configPath string) error {
	listen := ":8080"
	if cfg, err := config.Load(configPath); err == nil {
		listen = cfg.Web.Listen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("bad web.listen %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
