package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OpenDeskViewer/platform/api/internal/config"
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

	pgPool, err := postgres.New(ctx,
		cfg.PostgresHost, cfg.PostgresPort,
		cfg.PostgresDB, cfg.PostgresUser, cfg.PostgresPassword,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize PostgreSQL")
	}
	defer pgPool.Close()

	telemetryService := telemetry.NewService(pgPool, cfg)

	go telemetryService.HeartbeatWorker(ctx)
	go telemetryService.CleanupWorker(ctx)

	log.Info().Msg("Workers started")
	log.Info().Msg("Press Ctrl+C to stop")

	<-ctx.Done()
	log.Info().Msg("Worker stopped")
}
