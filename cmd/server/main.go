package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"equipment-telemetry-simulator/internal/handler"
	"equipment-telemetry-simulator/internal/model"
	"equipment-telemetry-simulator/internal/simulator"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	db, err := handler.DbConnect()
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}

	sqlDB, err := db.DB()
	if err != nil {
		logger.Error("database handle unavailable", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			logger.Warn("database close failed", "error", err)
		}
	}()

	tickInterval := durationFromEnv("TICK_INTERVAL", 2*time.Second)
	pushInterval := durationFromEnv("PUSH_INTERVAL", tickInterval)

	var pushClient *simulator.PushClient
	if boolFromEnv("PUSH_MODE") {
		targetURL := strings.TrimSpace(os.Getenv("TARGET_TOIR_URL"))
		if targetURL == "" {
			logger.Error("PUSH_MODE=true requires TARGET_TOIR_URL")
			os.Exit(1)
		}
		ingestSecret := strings.TrimSpace(os.Getenv("TOIR_TELEMETRY_INGEST_SECRET"))
		if ingestSecret == "" {
			logger.Error("PUSH_MODE=true requires TOIR_TELEMETRY_INGEST_SECRET")
			os.Exit(1)
		}
		pushClient = simulator.NewPushClient(targetURL, ingestSecret)
		logger.Info("telemetry push enabled", "target", targetURL, "interval", pushInterval.String())
	}

	manager, err := simulator.NewManager(db, simulator.Config{
		TickInterval: tickInterval,
		PushClient:   pushClient,
		PushInterval: pushInterval,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("manager initialization failed", "error", err)
		os.Exit(1)
	}

	seedDefaults(manager, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager.Start(ctx)

	api := handler.NewAPI(manager, logger)
	server := &http.Server{
		Addr:              ":" + stringFromEnv("PORT", "8080"),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("equipment telemetry simulator listening", "addr", server.Addr, "tickInterval", tickInterval.String())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

func seedDefaults(manager *simulator.Manager, logger *slog.Logger) {
	defaultTypes := []model.AssetTypeDefinition{
		{
			ID:          "WATER_PUMP",
			Name:        "Water Pump",
			Description: "Industrial pump with pressure and temperature telemetry",
			Metrics: model.MetricDefinitions{
				{Name: "temperature_c", Unit: "C", Min: 50, Max: 75, Drift: 1.2},
				{Name: "pressure_bar", Unit: "bar", Min: 3.5, Max: 4.5, Drift: 0.12},
			},
			FaultTypes: []string{"OVERHEATING", "LOW_PRESSURE", "HIGH_PRESSURE"},
		},
		{
			ID:          "COMPRESSOR",
			Name:        "Air Compressor",
			Description: "Compressor reporting temperature, pressure, and output volume",
			Metrics: model.MetricDefinitions{
				{Name: "temperature_c", Unit: "C", Min: 60, Max: 85, Drift: 1.5},
				{Name: "pressure_psi", Unit: "psi", Min: 90, Max: 120, Drift: 2.5},
				{Name: "volume_m3_min", Unit: "m3/min", Min: 8, Max: 14, Drift: 0.5},
			},
			FaultTypes: []string{"OVERHEATING", "PRESSURE_DROP", "VOLUME_DROP"},
		},
	}

	for _, definition := range defaultTypes {
		if _, err := manager.CreateAssetType(definition); err != nil {
			logger.Debug("seed asset type skipped", "assetTypeId", definition.ID, "error", err)
		}
	}

	defaultAssets := []struct {
		assetID     string
		assetTypeID string
	}{
		{assetID: "PUMP-101", assetTypeID: "WATER_PUMP"},
		{assetID: "PUMP-102", assetTypeID: "WATER_PUMP"},
		{assetID: "COMP-301", assetTypeID: "COMPRESSOR"},
	}

	for _, asset := range defaultAssets {
		if _, err := manager.RegisterAsset(asset.assetID, asset.assetTypeID); err != nil {
			logger.Debug("seed asset skipped", "assetId", asset.assetID, "error", err)
		}
	}
}

func stringFromEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func boolFromEnv(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}
