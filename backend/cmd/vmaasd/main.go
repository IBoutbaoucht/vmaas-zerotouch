// Command vmaasd is the VMaaS provisioning daemon.
//
//	vmaasd --config /etc/vmaas/vmaas.yaml
//
// It loads config, opens the bbolt state store, logs into ESXi, then starts
// the HTTP API. On SIGTERM/SIGINT it stops accepting new requests, waits for
// in-flight provisions to finish, and exits cleanly.
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
	"syscall"
	"time"

	"github.com/cuneyt/vmaas-engine/internal/api"
	"github.com/cuneyt/vmaas-engine/internal/cloudinit"
	"github.com/cuneyt/vmaas-engine/internal/config"
	"github.com/cuneyt/vmaas-engine/internal/esxi"
	"github.com/cuneyt/vmaas-engine/internal/ipalloc"
	"github.com/cuneyt/vmaas-engine/internal/lifecycle"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

func main() {
	var (
		configPath = flag.String("config", "/etc/vmaas/vmaas.yaml", "path to config file")
		once       = flag.Bool("once", false, "connect to ESXi, print AboutInfo, exit")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(*configPath, *once); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, once bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("connecting to esxi", "url", cfg.ESXi.URL)
	// Bound the initial login so a misconfigured .env (bad password,
	// unreachable host, wrong URL) fails fast with a clear error instead
	// of hanging on TCP retries.
	connectCtx, connectCancel := context.WithTimeout(ctx, 20*time.Second)
	ex, err := esxi.New(connectCtx, &cfg.ESXi)
	connectCancel()
	if err != nil {
		return fmt.Errorf("esxi: %w", err)
	}
	defer ex.Close(context.Background())

	about := ex.About(ctx)
	slog.Info("esxi connected",
		"product", about.Name,
		"version", about.Version,
		"build", about.Build,
		"api_type", about.ApiType,
	)

	if once {
		fmt.Printf("Name:        %s\nVersion:     %s\nBuild:       %s\nApiType:     %s\nApiVersion:  %s\nGoldVM:      %s\nDatastore:   %s\n",
			about.Name, about.Version, about.Build, about.ApiType, about.ApiVersion,
			ex.Gold.Name(), ex.Datastore.Name())
		return nil
	}

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()

	alloc, err := ipalloc.New(st, &cfg.Network)
	if err != nil {
		return fmt.Errorf("ipalloc: %w", err)
	}

	renderer, err := cloudinit.New(&cfg.CloudInit, &cfg.Network)
	if err != nil {
		return fmt.Errorf("cloudinit: %w", err)
	}

	orch := lifecycle.New(ex, alloc, st, renderer, cfg)

	server := api.New(cfg, orch, alloc, st, ex)
	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.Router(),
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	orch.Wait()
	slog.Info("bye")
	return nil
}
