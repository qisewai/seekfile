package indexer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"seekfile/internal/storage"
)

// FileRecord describes metadata captured for a file on disk.
type FileRecord struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modified"`
	RootPath string    `json:"rootPath"`
}

// Query defines the search criteria supported by the indexer.
type Query struct {
	NamePattern    string
	MinSize        int64
	MaxSize        int64
	ModifiedAfter  time.Time
	ModifiedBefore time.Time
	SortField      string
	SortDescending bool
	Offset         int
	Limit          int
	Extensions     []string
}

// SearchResult describes the outcome of a search request.
type SearchResult struct {
	Files []FileRecord
	Total int
}

// ScanMode indicates how a scan should be executed.
type ScanMode string

const (
	// ScanModeIncremental updates the index by checking for differences from the current snapshot.
	ScanModeIncremental ScanMode = "incremental"
	// ScanModeFull rebuilds the index from scratch.
	ScanModeFull ScanMode = "full"
)

// ErrScanInProgress is returned when attempting to start a scan while one is already running.
var ErrScanInProgress = errors.New("scan already in progress")

// ScanStatus summarizes the current or most recent scan activity.
type ScanStatus struct {
	Mode              string    `json:"mode"`
	Running           bool      `json:"running"`
	CurrentPath       string    `json:"currentPath"`
	Processed         int64     `json:"processed"`
	KnownFiles        int       `json:"knownFiles"`
	StartedAt         time.Time `json:"startedAt"`
	FinishedAt        time.Time `json:"finishedAt"`
	LastSuccessfulRun time.Time `json:"lastSuccessfulRun"`
	Error             string    `json:"error,omitempty"`
}

// RecordStore describes the persistence operations required by the indexer.
type RecordStore interface {
	LoadAll(ctx context.Context) ([]storage.Record, error)
	Upsert(ctx context.Context, record storage.Record) error
	Delete(ctx context.Context, path string) error
	ScanState(ctx context.Context, root string) (storage.ScanState, error)
	UpdateScanState(ctx context.Context, state storage.ScanState) error
}

// Indexer builds and maintains an in-memory representation of files on disk.
type Indexer struct {
	mu        sync.RWMutex
	files     map[string]FileRecord
	scanRoots []string

	store RecordStore

	statusMu sync.RWMutex
	status   ScanStatus

	scanMu     sync.Mutex
	scanCancel context.CancelFunc
}

// New constructs an Indexer for the provided root directories backed by the supplied store.
func New(scanRoots []string, store RecordStore) (*Indexer, error) {
	if len(scanRoots) == 0 {
		return nil, errors.New("at least one scan root is required")
	}

	normalized := make([]string, 0, len(scanRoots))
	for _, root := range scanRoots {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, filepath.Clean(abs))
	}
	if len(normalized) == 0 {
		return nil, errors.New("no valid scan roots provided")
	}

	return &Indexer{
		files:     make(map[string]FileRecord),
		scanRoots: normalized,
		store:     store,
	}, nil
}

// LoadFromStore restores the in-memory index from the persistent cache.
func (idx *Indexer) LoadFromStore(ctx context.Context) (int, error) {
	if idx.store == nil {
		return 0, nil
	}

	records, err := idx.store.LoadAll(ctx)
	if err != nil {
		return 0, err
	}

	data := make(map[string]FileRecord, len(records))
	for _, record := range records {
		normalized := filepath.Clean(record.Path)
		data[normalized] = FileRecord{
			Path:     normalized,
			Name:     record.Name,
			Size:     record.Size,
			ModTime:  record.ModTime,
			RootPath: record.RootPath,
		}
	}

	idx.mu.Lock()
	idx.files = data
	idx.mu.Unlock()

	var lastRun time.Time
	if idx.store != nil {
		for _, root := range idx.scanRoots {
			state, stateErr := idx.store.ScanState(ctx, root)
			if stateErr != nil {
				continue
			}
			if state.LastFullScan.After(lastRun) {
				lastRun = state.LastFullScan
			}
			if state.LastIncrementalScan.After(lastRun) {
				lastRun = state.LastIncrementalScan
			}
		}
	}

	idx.updateStatus(func(status *ScanStatus) {
		status.LastSuccessfulRun = lastRun
		status.Mode = string(ScanModeIncremental)
		status.Processed = 0
		status.Error = ""
	})

	return len(records), nil
}

// StartScan triggers a background scan using the provided mode. Only one scan may run at a time.
func (idx *Indexer) StartScan(ctx context.Context, mode ScanMode) error {
	if ctx == nil {
		ctx = context.Background()
	}

	idx.statusMu.Lock()
	if idx.status.Running {
		idx.statusMu.Unlock()
		return ErrScanInProgress
	}
	idx.status = ScanStatus{
		Mode:        string(mode),
		Running:     true,
		StartedAt:   time.Now(),
		Processed:   0,
		KnownFiles:  idx.countFiles(),
		FinishedAt:  time.Time{},
		Error:       "",
		CurrentPath: "",
	}
	idx.statusMu.Unlock()

	scanCtx, cancel := context.WithCancel(ctx)

	idx.scanMu.Lock()
	if idx.scanCancel != nil {
		idx.scanCancel()
	}
	idx.scanCancel = cancel
	idx.scanMu.Unlock()

	go idx.runScan(scanCtx, mode)
	return nil
}

// StopScan cancels an in-flight scan if one is running.
func (idx *Indexer) StopScan() {
	idx.scanMu.Lock()
	defer idx.scanMu.Unlock()
	if idx.scanCancel != nil {
		idx.scanCancel()
		idx.scanCancel = nil
	}
}

// Status returns a snapshot of the current scan status along with the number of indexed files.
func (idx *Indexer) Status() ScanStatus {
	idx.statusMu.RLock()
	status := idx.status
	idx.statusMu.RUnlock()

	status.KnownFiles = idx.countFiles()
	return status
}

// Search returns a slice of FileRecord that match the query parameters.
func (idx *Indexer) Search(ctx context.Context, query Query) SearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	nameMatcher := buildNameMatcher(query.NamePattern)

	allowedExts := make(map[string]struct{})
	if len(query.Extensions) > 0 {
		for _, ext := range query.Extensions {
			normalized := strings.ToLower(strings.TrimSpace(ext))
			if normalized == "" {
				continue
			}
			if !strings.HasPrefix(normalized, ".") {
				normalized = "." + normalized
			}
			allowedExts[normalized] = struct{}{}
		}
	}

	matches := make([]FileRecord, 0)
	for _, record := range idx.files {
		if ctx.Err() != nil {
			break
		}
		if !matchesQuery(record, query, nameMatcher, allowedExts) {
			continue
		}
		matches = append(matches, record)
	}

	sort.Slice(matches, func(i, j int) bool {
		cmp := compareRecords(matches[i], matches[j], query.SortField)
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(matches[i].Name), strings.ToLower(matches[j].Name))
			if cmp == 0 {
				if matches[i].Path == matches[j].Path {
					return false
				}
				return matches[i].Path < matches[j].Path
			}
		}

		if query.SortDescending {
			return cmp > 0
		}
		return cmp < 0
	})

	total := len(matches)

	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}

	limit := query.Limit
	if limit <= 0 || offset+limit > total {
		limit = total - offset
	}

	paged := matches
	if offset != 0 || limit != total {
		paged = matches[offset : offset+limit]
	}

	return SearchResult{Files: paged, Total: total}
}

// Lookup returns a FileRecord by its full path.
func (idx *Indexer) Lookup(path string) (FileRecord, bool) {
	normalized := filepath.Clean(path)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	record, ok := idx.files[normalized]
	return record, ok
}

// Roots returns the configured scan roots.
func (idx *Indexer) Roots() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	roots := make([]string, len(idx.scanRoots))
	copy(roots, idx.scanRoots)
	return roots
}

// UpdateFile updates metadata for a single file. It is intended to be used by
// filesystem watchers to keep the index fresh.
func (idx *Indexer) UpdateFile(record FileRecord) {
	_ = idx.saveRecord(context.Background(), record)
}

// RemoveFile removes a file from the index by its path.
func (idx *Indexer) RemoveFile(path string) {
	_ = idx.deleteRecord(context.Background(), path)
}

func (idx *Indexer) runScan(ctx context.Context, mode ScanMode) {
	defer func() {
		idx.scanMu.Lock()
		idx.scanCancel = nil
		idx.scanMu.Unlock()
	}()

	var firstErr error
	processed := int64(0)
	seen := make(map[string]struct{})
	scannedRoots := make(map[string]struct{})
	rootStates := make(map[string]storage.ScanState)
	if idx.store != nil {
		for _, root := range idx.scanRoots {
			state, err := idx.store.ScanState(ctx, root)
			if err != nil {
				continue
			}
			rootStates[root] = state
		}
	}

	for _, root := range idx.scanRoots {
		select {
		case <-ctx.Done():
			firstErr = ctx.Err()
			idx.updateStatus(func(status *ScanStatus) {
				status.Error = ctx.Err().Error()
			})
			break
		default:
		}

		if err := idx.walkRoot(ctx, root, mode, seen, &processed); err != nil {
			if errors.Is(err, context.Canceled) {
				firstErr = ctx.Err()
				break
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		scannedRoots[root] = struct{}{}

		if idx.store != nil {
			timestamp := time.Now()
			state := rootStates[root]
			switch mode {
			case ScanModeFull:
				state.LastFullScan = timestamp
				state.LastIncrementalScan = timestamp
			default:
				state.LastIncrementalScan = timestamp
			}
			state.RootPath = root
			if err := idx.store.UpdateScanState(ctx, state); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	if len(scannedRoots) > 0 {
		if err := idx.removeMissing(ctx, seen, scannedRoots); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	finish := time.Now()

	idx.updateStatus(func(status *ScanStatus) {
		status.Running = false
		status.FinishedAt = finish
		status.Processed = processed
		status.CurrentPath = ""
		if firstErr != nil {
			status.Error = firstErr.Error()
		} else {
			status.Error = ""
			status.LastSuccessfulRun = finish
		}
	})
}

func (idx *Indexer) walkRoot(ctx context.Context, root string, mode ScanMode, seen map[string]struct{}, processed *int64) error {
	walker := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if entry.IsDir() {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}

		normalized := filepath.Clean(path)
		*processed++
		seen[normalized] = struct{}{}

		idx.updateStatus(func(status *ScanStatus) {
			status.Processed = *processed
			status.CurrentPath = normalized
		})

		if mode == ScanModeIncremental {
			if existing, ok := idx.Lookup(normalized); ok {
				if existing.Size == info.Size() && existing.ModTime.Equal(info.ModTime()) {
					return nil
				}
			}
		}

		record := FileRecord{
			Path:     normalized,
			Name:     info.Name(),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			RootPath: root,
		}

		if err := idx.saveRecord(ctx, record); err != nil {
			return err
		}

		return nil
	}

	return filepath.WalkDir(root, walker)
}

func (idx *Indexer) removeMissing(ctx context.Context, seen map[string]struct{}, scannedRoots map[string]struct{}) error {
	idx.mu.RLock()
	candidates := make([]string, 0)
	for path, record := range idx.files {
		if _, ok := scannedRoots[record.RootPath]; !ok {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		candidates = append(candidates, path)
	}
	idx.mu.RUnlock()

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err := idx.deleteRecord(ctx, path); err != nil {
			return err
		}
	}

	return nil
}

func (idx *Indexer) saveRecord(ctx context.Context, record FileRecord) error {
	normalized := filepath.Clean(record.Path)
	record.Path = normalized

	idx.mu.Lock()
	idx.files[normalized] = record
	idx.mu.Unlock()

	if idx.store == nil {
		return nil
	}

	storageRecord := storage.Record{
		Path:     record.Path,
		Name:     record.Name,
		Size:     record.Size,
		ModTime:  record.ModTime,
		RootPath: record.RootPath,
	}

	return idx.store.Upsert(ctx, storageRecord)
}

func (idx *Indexer) deleteRecord(ctx context.Context, path string) error {
	normalized := filepath.Clean(path)

	idx.mu.Lock()
	delete(idx.files, normalized)
	idx.mu.Unlock()

	if idx.store == nil {
		return nil
	}

	return idx.store.Delete(ctx, normalized)
}

func (idx *Indexer) countFiles() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.files)
}

func (idx *Indexer) updateStatus(update func(*ScanStatus)) {
	idx.statusMu.Lock()
	update(&idx.status)
	idx.statusMu.Unlock()
}

func matchesQuery(record FileRecord, query Query, matchName func(string) bool, allowedExts map[string]struct{}) bool {
	if matchName != nil && !matchName(record.Name) {
		return false
	}
	if len(allowedExts) > 0 {
		ext := strings.ToLower(filepath.Ext(record.Name))
		if ext == "" {
			return false
		}
		if _, ok := allowedExts[ext]; !ok {
			return false
		}
	}
	if query.MinSize > 0 && record.Size < query.MinSize {
		return false
	}
	if query.MaxSize > 0 && record.Size > query.MaxSize {
		return false
	}
	if !query.ModifiedAfter.IsZero() && record.ModTime.Before(query.ModifiedAfter) {
		return false
	}
	if !query.ModifiedBefore.IsZero() && record.ModTime.After(query.ModifiedBefore) {
		return false
	}
	return true
}

func buildNameMatcher(pattern string) func(string) bool {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil
	}

	lowered := strings.ToLower(trimmed)
	if strings.ContainsAny(lowered, "*?") {
		regexPattern := wildcardToRegex(lowered)
		if regexPattern != "" {
			if re, err := regexp.Compile("^" + regexPattern + "$"); err == nil {
				return func(name string) bool {
					return re.MatchString(strings.ToLower(name))
				}
			}
		}
	}

	return func(name string) bool {
		return strings.Contains(strings.ToLower(name), lowered)
	}
}

func wildcardToRegex(pattern string) string {
	if pattern == "" {
		return ""
	}
	escaped := regexp.QuoteMeta(pattern)
	escaped = strings.ReplaceAll(escaped, "\\*", ".*")
	escaped = strings.ReplaceAll(escaped, "\\?", ".")
	return escaped
}

func compareRecords(a, b FileRecord, field string) int {
	switch strings.ToLower(field) {
	case "size":
		switch {
		case a.Size < b.Size:
			return -1
		case a.Size > b.Size:
			return 1
		default:
			return 0
		}
	case "modified", "time":
		switch {
		case a.ModTime.Before(b.ModTime):
			return -1
		case a.ModTime.After(b.ModTime):
			return 1
		default:
			return 0
		}
	case "path":
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	default:
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	}
}

// ParseScanMode validates a scan mode string and falls back to the incremental mode when empty.
func ParseScanMode(mode string) (ScanMode, error) {
	if mode == "" {
		return ScanModeIncremental, nil
	}
	switch strings.ToLower(mode) {
	case string(ScanModeFull):
		return ScanModeFull, nil
	case string(ScanModeIncremental):
		return ScanModeIncremental, nil
	default:
		return "", fmt.Errorf("unknown scan mode %q", mode)
	}
}
