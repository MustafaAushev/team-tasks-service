// Command api запускает HTTP-сервер сервиса командных задач.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/secunda/team-tasks-service/api"
	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/cache"
	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/httpapi"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/statistics"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
)

func main() {
	logger := platform.NewLogger(os.Stdout, slog.LevelInfo)
	if err := run(logger); err != nil {
		logger.Error("service stopped with error",
			"context", "system", "request_id", nil,
			"meta", map[string]any{"event": "fatal", "error": err.Error()})
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	getenv, err := config.Getenv(config.DotEnvFile)
	if err != nil {
		return err
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := mysqlstore.Open(startCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer db.Close()

	rc := cache.NewClient(cfg)
	defer rc.Close()

	clock := platform.SystemClock{}
	ids := platform.UUIDv7Generator{}
	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTTTL, clock)
	authSvc, err := auth.NewService(mysqlstore.UserStore{DB: db}, tokens, ids, clock, cfg.BcryptCost)
	if err != nil {
		return err
	}

	ops := httpapi.NewOperations(httpapi.Services{
		Logger: logger,
		Auth:   authSvc,
		Teams:  team.NewService(mysqlstore.TeamStore{DB: db}, ids, clock),
		Tasks: task.NewService(mysqlstore.TaskStore{DB: db}, ids, clock, cfg.CursorSecret).
			WithListCache(cache.NewListCache(rc, cfg, clock, logger)),
		Stats: statistics.NewService(mysqlstore.StatsStore{DB: db}, clock),
	})

	handler := httpapi.NewHandler(httpapi.Deps{
		Logger:         logger,
		Tokens:         tokens,
		DB:             db,
		RequestTimeout: cfg.RequestTimeout,
		Ops:            ops,
		Limits: httpapi.RateLimits{
			UserPerMinute: cfg.RateUserPerMinute, UserBurst: cfg.RateUserBurst,
			AuthPerMinute: cfg.RateAuthPerMinute, AuthBurst: cfg.RateAuthBurst,
			TrustProxyHeaders: cfg.TrustProxyHeaders,
		},
		Clock:   clock,
		OpenAPI: api.OpenAPISpec,
	})

	// Контекст запроса не прерывает чтение тела: ReadTimeout задаёт дедлайн соединения.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "context", "system", "request_id", nil,
			"meta", map[string]any{"event": "listen", "addr": cfg.HTTPAddr})
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down", "context", "system", "request_id", nil, "meta", map[string]any{"event": "shutdown"})
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}
