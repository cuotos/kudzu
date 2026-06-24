// Command kudzu runs the deployment-gate service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bloomandwild/kudzu/internal/config"
	"github.com/bloomandwild/kudzu/internal/gate"
	gh "github.com/bloomandwild/kudzu/internal/github"
	"github.com/bloomandwild/kudzu/internal/httpapi"
	"github.com/bloomandwild/kudzu/internal/observability"
	redisstore "github.com/bloomandwild/kudzu/internal/store/redis"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.WriteTokens) == 0 {
		log.Warn("no KUDZU_WRITE_TOKENS configured: all write endpoints will reject requests")
	}

	rdb := goredis.NewClient(&goredis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return errors.New("cannot reach redis at " + cfg.RedisAddr + ": " + err.Error())
	}

	var evicter gate.Evicter = gate.NoopEvicter{}
	if cfg.EvictionEnabled() {
		client, err := gh.New(cfg.GitHubAppID, cfg.GitHubInstallationID, cfg.GitHubPrivateKey, cfg.GitHubAPIBaseURL, log)
		if err != nil {
			return err
		}
		evicter = client
		log.Info("proactive merge-group eviction enabled", "app_id", cfg.GitHubAppID)
	} else {
		log.Info("proactive eviction disabled (no GitHub App configured); relying on next gate poll")
	}

	svc := gate.NewService(redisstore.New(rdb), evicter, gate.Config{
		FailureThreshold: cfg.FailureThreshold,
		CheckContext:     cfg.CheckContext,
	}, log)

	metrics := observability.New(svc, log)

	router := httpapi.NewRouter(httpapi.Options{
		Service:         svc,
		Metrics:         metrics,
		MetricsHandler:  metrics.Handler(),
		WriteTokens:     cfg.WriteTokens,
		RequireReadAuth: cfg.RequireReadAuth,
		Log:             log,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: httpapi.DefaultReadTimeout,
		ReadTimeout:       httpapi.DefaultReadTimeout,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("kudzu listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
