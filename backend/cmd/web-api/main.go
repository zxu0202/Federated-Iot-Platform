// Command web-api starts the S1 PostgreSQL Web/API control plane.
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zx/federated-iot-platform/backend/internal/config"
	"github.com/zx/federated-iot-platform/backend/internal/healthcheck"
	"github.com/zx/federated-iot-platform/backend/internal/httpapi"
	"github.com/zx/federated-iot-platform/backend/internal/parameters"
	"github.com/zx/federated-iot-platform/backend/internal/recovery"
	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

var (
	version  = "dev"
	revision = "unknown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck.Execute(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration rejected", "error", err.Error())
		os.Exit(2)
	}
	if cfg.ServiceVersion == "dev" && version != "" {
		cfg.ServiceVersion = version
	}
	ctx := context.Background()
	if len(os.Args) == 3 && os.Args[1] == "migrate" {
		direction := postgres.MigrationDirection(os.Args[2])
		if direction != postgres.MigrationUp && direction != postgres.MigrationDown {
			logger.Error("migration direction must be up or down")
			os.Exit(2)
		}
		if cfg.MigrationDatabaseURL == "" {
			logger.Error("IOT_MIGRATION_DATABASE_URL is required for the dedicated migration service")
			os.Exit(2)
		}
		migrationPool, err := pgxpool.New(ctx, cfg.MigrationDatabaseURL)
		if err != nil {
			logger.Error("migration database pool creation failed", "error", err.Error())
			os.Exit(2)
		}
		defer migrationPool.Close()
		if err := migrationPool.Ping(ctx); err != nil {
			logger.Error("migration database connection failed", "error", err.Error())
			os.Exit(2)
		}
		if err := postgres.Migrate(ctx, migrationPool, direction); err != nil {
			logger.Error("migration failed", "error", err.Error())
			os.Exit(1)
		}
		logger.Info("migration completed", "direction", direction)
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		logger.Error("usage: web-api [serve|migrate up|migrate down|healthcheck --kind=live --config PATH]")
		os.Exit(2)
	}
	if cfg.DatabaseURL == "" {
		logger.Error("IOT_DATABASE_URL is required")
		os.Exit(2)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database pool creation failed", "error", err.Error())
		os.Exit(2)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		logger.Error("database connection failed", "error", err.Error())
		os.Exit(2)
	}
	if err := postgres.VerifyCurrentSchema(ctx, pool); err != nil {
		logger.Error("required schema is not current", "error", err.Error())
		os.Exit(1)
	}
	if err := cfg.ValidateSimulationRuntime(); err != nil {
		logger.Error("simulation runtime identity rejected", "error", err.Error())
		os.Exit(2)
	}
	repository := postgres.New(pool, postgres.RuntimeIdentity{AlgorithmVersion: cfg.AlgorithmVersion, WorkerVersion: cfg.WorkerVersion, WorkerImageDigest: cfg.WorkerImageDigest, NumericRuntime: cfg.NumericRuntime})
	parameterConstraints, parameterConstraintsErr := parameters.LoadFile(cfg.ParameterConstraintsFile)
	repository.ConfigureParameterConstraints(parameterConstraints, parameterConstraintsErr)
	if err := repository.EnsureReferenceProfiles(ctx); err != nil {
		logger.Error("reference profile initialization failed", "error", err.Error())
		os.Exit(1)
	}
	leaseRecovery, err := recovery.New(repository, leaseRecoveryInterval(cfg.HeartbeatInterval, cfg.LeaseDuration), func(recovered int64, err error) {
		if err != nil {
			logger.Error("expired Worker lease recovery failed", "error", err.Error())
			return
		}
		if recovered > 0 {
			logger.Warn("expired Worker leases recovered", "count", recovered)
		}
	})
	if err != nil {
		logger.Error("lease recovery configuration rejected", "error", err.Error())
		os.Exit(2)
	}
	if recovered, err := leaseRecovery.RecoverOnce(ctx); err != nil {
		logger.Error("startup expired Worker lease recovery failed", "error", err.Error())
		os.Exit(1)
	} else if recovered > 0 {
		logger.Warn("startup expired Worker leases recovered", "count", recovered)
	}
	recoveryContext, cancelRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan struct{})
	go func() {
		leaseRecovery.Run(recoveryContext)
		close(recoveryDone)
	}()

	server := &http.Server{Addr: cfg.HTTPAddress, Handler: httpapi.New(cfg, repository, logger).Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		logger.Info("web api listening", "address", cfg.HTTPAddress, "version", cfg.ServiceVersion)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("web api stopped unexpectedly", "error", err.Error())
			os.Exit(1)
		}
	}()
	<-stop
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	cancelRecovery()
	select {
	case <-recoveryDone:
	case <-shutdown.Done():
		logger.Warn("lease recovery did not stop before shutdown timeout")
	}
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("graceful shutdown failed", "error", err.Error())
		_ = server.Close()
	}
	fmt.Fprintln(os.Stdout, `{"level":"INFO","event":"web_api_stopped"}`)
}

func leaseRecoveryInterval(heartbeat, lease time.Duration) time.Duration {
	interval := heartbeat
	if interval <= 0 || interval > lease/2 {
		interval = lease / 2
	}
	if interval <= 0 {
		return time.Second
	}
	return interval
}
