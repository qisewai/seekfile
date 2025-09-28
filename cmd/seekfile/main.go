package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"seekfile/internal/app"
	"seekfile/internal/config"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := config.FromFlags()
	if err != nil {
		log.Fatalf("parse config: %v", err)
	}

	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("initialize app: %v", err)
	}
	defer application.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
		log.Fatalf("application error: %v", err)
	}
}
