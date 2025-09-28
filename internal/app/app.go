package app

import (
	"context"
	"fmt"
	"log"

	"seekfile/internal/config"
	"seekfile/internal/frontend"
	"seekfile/internal/indexer"
	"seekfile/internal/server"
	sqlitestore "seekfile/internal/storage/sqlite"
)

// App ties together configuration, the indexer, and the HTTP server.
type App struct {
	cfg     config.Config
	indexer *indexer.Indexer
	server  *server.Server
	store   *sqlitestore.Store
}

// New constructs an App using the provided configuration.
func New(cfg config.Config) (*App, error) {
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open index store: %w", err)
	}

	idx, err := indexer.New(cfg.ScanPaths, store)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create indexer: %w", err)
	}

	renderer := frontend.NewRenderer()
	srv := server.New(idx, renderer)

	return &App{cfg: cfg, indexer: idx, server: srv, store: store}, nil
}

// Run boots the indexer and starts the HTTP server until the context is cancelled.
func (a *App) Run(ctx context.Context) error {
	log.Printf("loading cached index from %s", a.cfg.DatabasePath)
	loaded, err := a.indexer.LoadFromStore(ctx)
	if err != nil {
		return fmt.Errorf("load cached index: %w", err)
	}

	log.Printf("restored %d indexed files from cache", loaded)

	initialMode := indexer.ScanModeIncremental
	if a.cfg.RebuildOnStart {
		initialMode = indexer.ScanModeFull
	}

	if err := a.indexer.StartScan(ctx, initialMode); err != nil && err != indexer.ErrScanInProgress {
		return fmt.Errorf("start initial scan: %w", err)
	}

	log.Printf("starting server on %s", a.cfg.ListenAddr)
	if err := a.server.Start(ctx, a.cfg.ListenAddr); err != nil {
		return fmt.Errorf("run server: %w", err)
	}

	return nil
}

// Indexer exposes the underlying indexer instance for future integrations.
func (a *App) Indexer() *indexer.Indexer {
	return a.indexer
}

// Close releases resources held by the application.
func (a *App) Close() error {
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}
