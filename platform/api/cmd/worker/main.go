package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/monitoring"
	"github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	cfg, err := config.LoadConfig(".env")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})

	fmt.Println("OpenDeskViewer Worker starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The worker connects as the schema owner, and the API deliberately does not.
	//
	// Audit retention has to delete audit rows, and the whole point of migration
	// 000008 is that the request-serving role may not. So the one process that
	// legitimately removes evidence is also the one with no HTTP surface: it
	// listens on nothing and reads no user input, which is what makes this an
	// acceptable split rather than a hole. platform/README.md states it.
	pgPool, err := postgres.New(ctx,
		cfg.PostgresHost, cfg.PostgresPort,
		cfg.PostgresDB, cfg.PostgresUser, cfg.PostgresPassword,
		cfg.PostgresSSLMode,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize PostgreSQL")
	}
	defer pgPool.Close()

	monitoringService := monitoring.New(pgPool)
	telemetryService := telemetry.NewService(pgPool, cfg, fleet.NewService(pgPool, cfg), monitoringService)

	go telemetryService.HeartbeatWorker(ctx)
	go telemetryService.CleanupWorker(ctx)
	// Delivery is its own loop on its own interval: a slow webhook receiver
	// must not delay the recomputation that produces the events it delivers.
	go telemetryService.NotificationWorker(ctx)

	log.Info().Msg("Workers started")
	log.Info().Msg("Press Ctrl+C to stop")

	<-ctx.Done()
	log.Info().Msg("Worker stopped")
}
