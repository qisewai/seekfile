package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
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

	// DatabasePath specifies where the on-disk index cache is stored.
	DatabasePath string
}

// FromFlags parses configuration from command line flags. It should be called
// by the main package to construct the initial configuration for the
// application.
func FromFlags() (Config, error) {
	var configPath string

	flag.StringVar(&configPath, "config", "seekfile.config.json", "path to JSON configuration file")
	flag.Parse()

	return FromFile(configPath)
}

// FromFile loads the application configuration from the provided JSON file
// path. Relative scan paths are resolved relative to the configuration file's
// directory, ensuring they work regardless of the process working directory.
func FromFile(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("configuration file path cannot be empty")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve configuration path %q: %w", path, err)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration file %q: %w", absPath, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var raw struct {
		ListenAddr     string   `json:"listen_addr"`
		ScanPaths      []string `json:"scan_paths"`
		RebuildOnStart bool     `json:"rebuild_on_start"`
		DatabasePath   string   `json:"database_path"`
	}

	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	baseDir := filepath.Dir(absPath)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve configuration directory %q: %w", baseDir, err)
	}

	paths, err := normalizeScanPaths(raw.ScanPaths, baseAbs)
	if err != nil {
		return Config{}, err
	}

	databasePath := strings.TrimSpace(raw.DatabasePath)
	if databasePath == "" {
		databasePath = filepath.Join(baseAbs, "seekfile.db")
	} else if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(baseAbs, databasePath)
	}

	dbAbs, err := filepath.Abs(databasePath)
	if err != nil {
		return Config{}, fmt.Errorf("resolve database path %q: %w", databasePath, err)
	}

	cfg := Config{
		ListenAddr:     strings.TrimSpace(raw.ListenAddr),
		ScanPaths:      paths,
		RebuildOnStart: raw.RebuildOnStart,
		DatabasePath:   filepath.Clean(dbAbs),
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}

	return cfg, nil
}

func normalizeScanPaths(raw []string, baseDir string) ([]string, error) {
	normalized := make([]string, 0, len(raw))
	for _, part := range raw {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		candidate := trimmed
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(baseDir, candidate)
		}

		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve scan path %q: %w", trimmed, err)
		}

		normalized = append(normalized, filepath.Clean(abs))
	}

	if len(normalized) == 0 {
		normalized = append(normalized, filepath.Clean(baseDir))
	}

	return normalized, nil
}
