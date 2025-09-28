package config

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
)

// Config captures runtime configuration for the seekfile application.
type Config struct {
	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string

	// ScanPaths are the root directories that will be indexed and watched for changes.
	ScanPaths []string

	// RebuildOnStart forces the index to rebuild even if cached data is available.
	// The flag is included for future extensibility and currently has no effect
	// beyond signaling intent.
	RebuildOnStart bool
}

// FromFlags parses configuration from command line flags. It should be called
// by the main package to construct the initial configuration for the
// application.
func FromFlags() (Config, error) {
	var cfg Config
	var scanPaths string

	flag.StringVar(&cfg.ListenAddr, "listen", ":8080", "HTTP listen address")
	flag.StringVar(&scanPaths, "scan-paths", ".", "comma separated list of directories to index")
	flag.BoolVar(&cfg.RebuildOnStart, "reindex", false, "force rebuilding the index on start")
	flag.Parse()

	paths, err := normalizeScanPaths(scanPaths)
	if err != nil {
		return Config{}, err
	}
	cfg.ScanPaths = paths

	return cfg, nil
}

func normalizeScanPaths(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		abs, err := filepath.Abs(trimmed)
		if err != nil {
			return nil, fmt.Errorf("resolve scan path %q: %w", trimmed, err)
		}
		normalized = append(normalized, filepath.Clean(abs))
	}

	if len(normalized) == 0 {
		abs, err := filepath.Abs(".")
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		normalized = append(normalized, filepath.Clean(abs))
	}

	return normalized, nil
}
