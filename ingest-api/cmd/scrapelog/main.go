package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/dilutedev/scrapelog/ingest-api/internal/alerts"
	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/archive"
	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/httpapi"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
	"github.com/dilutedev/scrapelog/ingest-api/internal/query"
	"github.com/dilutedev/scrapelog/ingest-api/internal/storage"
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
		case "keys":
			return keys(arguments[1:])
		}
	}
	fmt.Fprintln(os.Stderr, "usage: scrapelog serve|healthcheck|keys")
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

	readerOptions, err := clickhouse.ParseDSN(loadedConfig.ClickHouseURL)
	if err != nil {
		logger.Error("parsing clickhouse url failed", "error", err)
		return 1
	}
	readerOptions.Auth.Database = loadedConfig.ClickHouseDatabase
	readerOptions.Auth.Username = loadedConfig.ClickHouseReaderUser
	readerOptions.Auth.Password = loadedConfig.ClickHouseReaderPassword

	readerConnection, err := clickhouse.Open(readerOptions)
	if err != nil {
		logger.Error("opening clickhouse reader connection failed", "error", err)
		return 1
	}
	defer readerConnection.Close()

	archiverOptions, err := clickhouse.ParseDSN(loadedConfig.ClickHouseURL)
	if err != nil {
		logger.Error("parsing clickhouse url failed", "error", err)
		return 1
	}
	archiverOptions.Auth.Database = loadedConfig.ClickHouseDatabase
	archiverOptions.Auth.Username = loadedConfig.ClickHouseArchiverUser
	archiverOptions.Auth.Password = loadedConfig.ClickHouseArchiverPassword

	archiverConnection, err := clickhouse.Open(archiverOptions)
	if err != nil {
		logger.Error("opening clickhouse archiver connection failed", "error", err)
		return 1
	}
	defer archiverConnection.Close()

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
	storageStore := storage.NewStore(connection)
	diskGuard := storage.NewGuard(storageStore, uint64(loadedConfig.ClickHouseDiskBudgetBytes))
	diskStartupContext, cancelDiskStartup := context.WithTimeout(signalContext, 10*time.Second)
	err = diskGuard.Refresh(diskStartupContext, logger)
	cancelDiskStartup()
	if err != nil {
		logger.Error("measuring clickhouse disk usage failed", "error", err)
		return 1
	}
	recordWriter := ingest.NewWriter(connection)
	requestReader := query.NewReader(readerConnection)
	var bodyUploader bodies.Uploader
	var bodyGetter httpapi.BodyGetter
	var archiveDownloader httpapi.ArchiveDownloader
	archivingEnabled := loadedConfig.S3Endpoint != "" && !strings.HasSuffix(loadedConfig.S3Endpoint, "/")
	if loadedConfig.S3Endpoint != "" {
		s3Client := bodies.NewS3Uploader(loadedConfig.S3Endpoint, loadedConfig.S3Bucket, loadedConfig.S3Region, loadedConfig.S3AccessKeyID, loadedConfig.S3SecretAccessKey)
		bodyUploader = s3Client
		bodyGetter = s3Client
		if archivingEnabled {
			archiveDownloader = s3Client
		}
	}
	bodyStore := bodies.NewStore(bodyUploader, loadedConfig.S3BodyPrefix, logger)
	alertStore := alerts.NewStore(connection)
	alertCache := alerts.NewCache(alertStore)
	alertDispatcher := alerts.NewDispatcher(logger, alertStore, loadedConfig.PublicURL)
	alertStartupContext, cancelAlertStartup := context.WithTimeout(signalContext, 10*time.Second)
	if err := alertCache.Refresh(alertStartupContext); err != nil {
		logger.Error("loading alert rules failed", "error", err)
	}
	cancelAlertStartup()
	go keyCache.RunRefreshLoop(signalContext, logger)
	go alertCache.RunRefreshLoop(signalContext, logger)
	go alertDispatcher.Run()
	go diskGuard.RunRefreshLoop(signalContext, logger)
	var archiveLister httpapi.ArchiveLister
	if archivingEnabled {
		archiver := archive.NewArchiver(archiverConnection)
		go archiver.Run(signalContext, logger)
		archiveLister = archiver
	} else if loadedConfig.S3Endpoint != "" {
		logger.Warn("archiving disabled: S3_ENDPOINT must not end with a slash")
	}

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           httpapi.NewHandler(signalContext, logger, connection, keyStore, keyCache, recordWriter, bodyStore, storageStore, diskGuard, requestReader, bodyGetter, alertStore, alertCache, alertDispatcher, alertCache, alertDispatcher, archivingEnabled, archiveLister, archiveDownloader, loadedConfig),
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
	serverShutdownError := server.Shutdown(shutdownContext)
	dispatcherShutdownContext, cancelDispatcherShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDispatcherShutdown()
	alertDispatcher.Shutdown(dispatcherShutdownContext)
	if serverShutdownError != nil {
		logger.Error("shutdown failed", "error", serverShutdownError)
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
