// Command server runs the e-commerce application.
//
//	server [serve]                         start the HTTP server (default)
//	server migrate                         apply database migrations
//	server create-admin -email -name -role create an admin user (password from -password or ADMIN_PASSWORD)
//	server seed                            load demo catalog data and the first admin
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

	"github.com/redis/go-redis/v9"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/config"
	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/seed"
	"github.com/fuad71/ecommerce/internal/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "create-admin":
		err = createAdmin(os.Args[2:])
	case "seed":
		err = runSeed()
	default:
		err = fmt.Errorf("unknown command %q (use serve, migrate, create-admin or seed)", cmd)
	}
	if err != nil {
		slog.Error("fatal", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.AutoMigrate {
		if err := db.Migrate(pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		slog.Info("migrations applied")
	}

	ropts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis url: %w", err)
	}
	rdb := redis.NewClient(ropts)
	defer rdb.Close()

	ms, err := connectMedia(ctx, cfg)
	if err != nil {
		return err
	}

	srv, err := server.New(cfg, pool, rdb, ms)
	if err != nil {
		return err
	}
	srv.RunBackground(ctx)
	if cfg.SteadfastConfigured() {
		slog.Info("steadfast courier enabled", "auto_send", cfg.SteadfastAutoSend, "sync_interval", cfg.SteadfastSyncInterval.String())
	}
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// connectMedia retries briefly because MinIO may still be starting under docker compose.
func connectMedia(ctx context.Context, cfg *config.Config) (*media.Store, error) {
	var lastErr error
	for range 15 {
		ms, err := media.New(ctx, cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOBucket,
			cfg.MinIOUseSSL, cfg.MaxUploadBytes)
		if err == nil {
			return ms, nil
		}
		lastErr = err
		slog.Warn("waiting for object storage", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("object storage: %w", lastErr)
}

func migrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(pool); err != nil {
		return err
	}
	slog.Info("migrations applied")
	return nil
}

func createAdmin(args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	email := fs.String("email", "", "admin email")
	name := fs.String("name", "Administrator", "display name")
	password := fs.String("password", os.Getenv("ADMIN_PASSWORD"), "password (min 10 chars); defaults to $ADMIN_PASSWORD")
	role := fs.String("role", auth.RoleAdmin, "admin or staff")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(pool); err != nil {
		return err
	}
	a, err := auth.NewService(pool).Create(ctx, *email, *name, *password, *role)
	if err != nil {
		return err
	}
	slog.Info("admin created", "id", a.ID, "email", a.Email, "role", a.Role)
	return nil
}

func runSeed() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(pool); err != nil {
		return err
	}
	ms, err := connectMedia(ctx, cfg)
	if err != nil {
		return err
	}
	return seed.Run(ctx, pool, ms)
}
