package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Set by build.sh (-ldflags -X main.version=...).
var version = "dev"

func main() {
	configPath := flag.String("config", "config.json", "path to the configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}
	d, err := buildDeps(cfg)
	if err != nil {
		log.Error("cannot start", "error", err.Error())
		os.Exit(1)
	}
	if cfg.Auth.IntrospectionURL == "" {
		log.Warn("callers are not authenticated: every backend is configured with auth \"none\"")
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newFuse(cfg, log, version, d),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No write timeout: a tool call can legitimately run for minutes and its response is streamed.
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Info("listening", "addr", cfg.Listen, "version", version, "backends", len(cfg.Backends))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

// buildDeps opens what the configuration points at: the encrypted state and the means of identifying callers.
func buildDeps(cfg Config) (deps, error) {
	d := deps{clientSecrets: map[string]string{}}
	for _, b := range cfg.Backends {
		if b.ClientSecretFile != "" {
			raw, err := os.ReadFile(b.ClientSecretFile)
			if err != nil {
				return deps{}, err
			}
			d.clientSecrets[b.Name] = strings.TrimSpace(string(raw))
		}
	}
	for _, b := range cfg.Backends {
		if b.oauth() {
			secret, err := readSecret(cfg.SecretFile)
			if err != nil {
				return deps{}, err
			}
			st, err := openStore(cfg.StateFile, secret)
			if err != nil {
				return deps{}, err
			}
			d.secret, d.store = secret, st
			break
		}
	}
	if cfg.Auth.IntrospectionURL == "" {
		d.auth = anonymous{}
		return d, nil
	}
	clientSecret := ""
	if cfg.Auth.ClientSecretFile != "" {
		raw, err := os.ReadFile(cfg.Auth.ClientSecretFile)
		if err != nil {
			return deps{}, err
		}
		clientSecret = strings.TrimSpace(string(raw))
	}
	d.auth = newIntrospector(cfg.Auth.IntrospectionURL, cfg.Auth.ClientID, clientSecret, cfg.Resource)
	d.frontSecret = clientSecret
	return d, nil
}
