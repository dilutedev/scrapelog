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

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/httpapi"
)

const listenAddress = ":8080"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "serve":
			return serve()
		case "healthcheck":
			return healthcheck()
		}
	}
	fmt.Fprintln(os.Stderr, "usage: scrapelog serve|healthcheck")
	return 2
}

func serve() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	loadedConfig, err := config.Load()
	if err != nil {
		logger.Error("loading config failed", "error", err)
		return 1
	}

	connectionOptions, err := clickhouse.ParseDSN(loadedConfig.ClickHouseURL)
	if err != nil {
		logger.Error("parsing clickhouse url failed", "error", err)
		return 1
	}
	connectionOptions.Auth.Database = loadedConfig.ClickHouseDatabase
	connectionOptions.Auth.Username = loadedConfig.ClickHouseUser
	connectionOptions.Auth.Password = loadedConfig.ClickHousePassword

	connection, err := clickhouse.Open(connectionOptions)
	if err != nil {
		logger.Error("opening clickhouse connection failed", "error", err)
		return 1
	}
	defer connection.Close()

	signalContext, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignalHandling()

	keyStore := apikeys.NewStore(connection)
	keyCache := apikeys.NewCache(keyStore)
	startupContext, cancelStartup := context.WithTimeout(signalContext, 10*time.Second)
	err = keyCache.Refresh(startupContext)
	cancelStartup()
	if err != nil {
		logger.Error("loading api keys failed", "error", err)
		return 1
	}
	go keyCache.RunRefreshLoop(signalContext, logger)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           httpapi.NewHandler(logger, connection, keyStore, keyCache, loadedConfig.AdminToken),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()
	logger.Info("server started", "address", listenAddress)

	select {
	case serverError := <-serverErrors:
		if !errors.Is(serverError, http.ErrServerClosed) {
			logger.Error("server failed", "error", serverError)
			return 1
		}
		return 0
	case <-signalContext.Done():
	}

	logger.Info("shutdown started")
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("shutdown failed", "error", err)
		return 1
	}
	logger.Info("server stopped")
	return 0
}

func healthcheck() int {
	if err := checkHealth("http://127.0.0.1" + listenAddress + "/v1/health"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func checkHealth(healthURL string) error {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(healthURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", response.StatusCode)
	}
	return nil
}
