package app

import (
	"context"
	"fmt"
	"log"

	"seekfile/internal/config"
	"seekfile/internal/frontend"
	"seekfile/internal/indexer"
	"seekfile/internal/server"
)

// App ties together configuration, the indexer, and the HTTP server.
type App struct {
	cfg     config.Config
	indexer *indexer.Indexer
	server  *server.Server
}

// New constructs an App using the provided configuration.
func New(cfg config.Config) (*App, error) {
	idx, err := indexer.New(cfg.ScanPaths)
	if err != nil {
		return nil, fmt.Errorf("create indexer: %w", err)
	}

	renderer := frontend.NewRenderer()
	srv := server.New(idx, renderer)

	return &App{cfg: cfg, indexer: idx, server: srv}, nil
}

// Run boots the indexer and starts the HTTP server until the context is cancelled.
func (a *App) Run(ctx context.Context) error {
	log.Printf("building file index across %d roots", len(a.cfg.ScanPaths))
	if err := a.indexer.BuildInitialIndex(ctx); err != nil {
		return fmt.Errorf("build initial index: %w", err)
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
