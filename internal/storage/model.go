package storage

import "time"

// Record represents a persisted file entry.
type Record struct {
	Path     string
	Name     string
	Size     int64
	ModTime  time.Time
	RootPath string
}

// ScanState captures bookkeeping for the last scan times of a root path.
type ScanState struct {
	RootPath            string
	LastFullScan        time.Time
	LastIncrementalScan time.Time
}
