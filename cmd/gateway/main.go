// Command gateway 启动 API-Football 多 key 请求网关。
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

	"api-football-gateway/internal/admin"
	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/config"
	"api-football-gateway/internal/keypool"
	"api-football-gateway/internal/proxy"
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

	entries := make([]keypool.KeyEntry, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		entries = append(entries, keypool.KeyEntry{Label: k.Label, Key: k.Key})
	}
	pool := keypool.New(entries, keypool.Options{
		SwitchThreshold:   cfg.Scheduler.SwitchThreshold,
		RateLimitCooldown: time.Duration(cfg.Scheduler.RateLimitCooldownSeconds) * time.Second,
		ErrorCooldown:     time.Duration(cfg.Scheduler.ErrorCooldownSeconds) * time.Second,
		MaxErrorCount:     cfg.Scheduler.MaxErrorCount,
	})

	authenticator := auth.New(cfg.ClientAuth.Enabled, cfg.ClientAuth.Tokens)

	proxyHandler := proxy.NewHandler(proxy.Config{
		UpstreamBaseURL: cfg.Upstream.BaseURL,
		Timeout:         time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second,
		MaxRetries:      cfg.Scheduler.MaxRetries,
	}, pool, authenticator).WithLogger(logger)

	adminHandler := admin.New(pool)

	mux := http.NewServeMux()
	mux.Handle("/__gateway/", adminHandler)
	mux.Handle("/", proxyHandler)

	addr := cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // 防慢连接长期占用 goroutine
	}

	go func() {
		logger.Info("gateway listening",
			"addr", addr,
			"upstream", cfg.Upstream.BaseURL,
			"keys", len(cfg.Keys),
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
