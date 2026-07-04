package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sakawat-hossain/V2bX/internal/config"
	"github.com/Sakawat-hossain/V2bX/internal/service"
)

// RunServer loads the config at configPath and runs the agent in the
// foreground until it receives SIGINT/SIGTERM. SIGHUP triggers an immediate
// out-of-band panel resync (see ReloadService).
func RunServer(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	mgr, err := service.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			logger.Info("received SIGHUP, forcing immediate panel resync")
			mgr.Sync(ctx)
		}
	}()

	reportTicker := time.NewTicker(30 * time.Second)
	defer reportTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reportTicker.C:
				if err := mgr.PushStats(ctx); err != nil {
					logger.Warn("push stats failed", "error", err)
				}
				if err := mgr.ReportAlive(ctx); err != nil {
					logger.Warn("report alive failed", "error", err)
				}
			}
		}
	}()

	logger.Info("v2bx agent starting", "config", configPath)
	return mgr.Run(ctx)
}

func newLogger(lc config.LogConfig) *slog.Logger {
	var level slog.Level
	switch lc.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	out := os.Stdout
	if lc.Output != "" && lc.Output != "stdout" {
		f, err := os.OpenFile(lc.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			out = f
		}
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))
}
