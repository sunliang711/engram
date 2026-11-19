package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"engram/internal/admin"
	"engram/internal/auth"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/exporter"
	"engram/internal/mcp"
	"engram/internal/memory"
	"engram/internal/notify"
	"engram/internal/observability"
	"engram/internal/project"
	"engram/internal/version"
)

func main() {
	cfgPath := flag.String("config", "", "Path to TOML configuration file")
	exportOnce := flag.Bool("export-once", false, "Run deterministic Markdown export once and exit")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger := observability.NewLogger("error", os.Stderr)
		logger.Fatal().Err(err).Msg("failed to load configuration")
	}

	log := observability.NewLogger(cfg.Log.Level, os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize database")
	}
	defer database.Close()

	metrics := observability.NewMetrics()
	exp := exporter.NewExporter(database.Pool, cfg.Export, metrics, log)

	if *exportOnce {
		count, err := exp.ExportAll(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("markdown export failed")
		}
		log.Info().Int("exported_count", count).Str("output_dir", cfg.Export.OutputDir).Msg("markdown export completed")
		return
	}

	adminAuth := admin.NewAuthManager(database.Pool, cfg.Server.ContextSignKey, cfg.Server.ContextTTL, cfg.Auth.ResetAdmin, cfg.Auth.CookieSecure)
	if _, err := adminAuth.Bootstrap(ctx, os.Stdout); err != nil {
		log.Fatal().Err(err).Msg("failed to bootstrap web ui admin account")
	}

	authenticator, err := auth.NewAuthenticator(database.Pool, cfg.Auth)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize authenticator")
	}

	signer := auth.NewTokenSigner(cfg.Server.ContextSignKey, cfg.Server.ContextTTL)
	rateLimiter := auth.NewRateLimiter(cfg.Limits.WriteRatePerMinute, cfg.Limits.WriteBurst)
	resolver := project.NewResolver(database.Pool)
	hub := notify.NewHub(database.Pool, signer, log)

	go hub.RunListenLoop(ctx)
	go exp.RunPeriodicLoop(ctx)

	svc := memory.NewService(
		database.Pool,
		cfg,
		authenticator,
		signer,
		rateLimiter,
		resolver,
		hub,
		metrics,
		log,
	)

	mcpServer := mcp.NewServer(
		database,
		authenticator,
		signer,
		svc,
		hub,
		metrics,
		log,
	)

	adminHandler := admin.NewHandler(database.Pool, adminAuth, svc, exp, log)
	adminHandler.RegisterRoutes(mcpServer.Mux())

	httpServer := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      mcpServer.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("listen_addr", cfg.Server.ListenAddr).Str("version", version.Current()).Msg("starting Engram Server (MCP + Admin Web UI)")
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info().Msg("shutting down Engram Server")
	case err := <-errCh:
		log.Fatal().Err(err).Msg("http server failed")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("http server graceful shutdown error")
	}
}
