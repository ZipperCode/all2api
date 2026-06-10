// Command gateway starts the generic multi-upstream API gateway.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"all2api/internal/admin"
	"all2api/internal/config"
	"all2api/internal/gateway"
	"all2api/internal/logstore"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Logging.Level)
	slog.SetDefault(logger)

	events, err := logstore.New(cfg.Management.LogFile)
	if err != nil {
		logger.Error("failed to initialize log store", "error", err)
		os.Exit(1)
	}
	manager, err := gateway.New(*configPath, cfg, logger, events)
	if err != nil {
		logger.Error("failed to initialize gateway runtime", "error", err)
		os.Exit(1)
	}
	adminHandler := admin.NewConsole(manager, events)

	mux := http.NewServeMux()
	mux.Handle("/__admin/", adminHandler)
	mux.Handle("/__admin", adminHandler)
	mux.Handle("/__docs/", adminHandler)
	mux.Handle("/__docs", adminHandler)
	mux.Handle("/__gateway/", adminHandler)
	mux.Handle("/", manager)

	addr := cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // 防慢连接长期占用 goroutine
	}

	platformNames := make([]string, 0, len(cfg.Platforms))
	for name := range cfg.Platforms {
		platformNames = append(platformNames, name)
	}

	go func() {
		logger.Info("gateway listening",
			"addr", addr,
			"platforms", strings.Join(platformNames, ","),
			"credential_pools", len(cfg.CredentialPools),
			"client_auth", cfg.ClientAuth.Enabled)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

// newLogger 按级别构造 slog logger。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
