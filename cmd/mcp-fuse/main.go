// Command mcp-fuse presents several MCP servers as one.
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

	"github.com/daquv-core/mcp-fuse/internal/config"
	"github.com/daquv-core/mcp-fuse/internal/front"
	"github.com/daquv-core/mcp-fuse/internal/fuse"
	"github.com/daquv-core/mcp-fuse/internal/state"
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

	cfg, err := config.Load(*configPath)
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
		Handler:           fuse.New(cfg, log, version, d),
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
func buildDeps(cfg config.Config) (fuse.Deps, error) {
	d := fuse.Deps{ClientSecrets: map[string]string{}}
	for _, b := range cfg.Backends {
		if b.ClientSecretFile != "" {
			raw, err := os.ReadFile(b.ClientSecretFile)
			if err != nil {
				return fuse.Deps{}, err
			}
			d.ClientSecrets[b.Name] = strings.TrimSpace(string(raw))
		}
	}
	for _, b := range cfg.Backends {
		if b.OAuth() {
			secret, err := state.ReadSecret(cfg.SecretFile)
			if err != nil {
				return fuse.Deps{}, err
			}
			st, err := state.Open(cfg.StateFile, secret)
			if err != nil {
				return fuse.Deps{}, err
			}
			d.Secret, d.Store = secret, st
			break
		}
	}
	if cfg.Auth.IntrospectionURL == "" {
		d.Auth = front.Anonymous{}
		return d, nil
	}
	clientSecret := ""
	if cfg.Auth.ClientSecretFile != "" {
		raw, err := os.ReadFile(cfg.Auth.ClientSecretFile)
		if err != nil {
			return fuse.Deps{}, err
		}
		clientSecret = strings.TrimSpace(string(raw))
	}
	d.Auth = front.NewIntrospector(cfg.Auth.IntrospectionURL, cfg.Auth.ClientID, clientSecret, cfg.Resource)
	d.FrontSecret = clientSecret
	return d, nil
}
