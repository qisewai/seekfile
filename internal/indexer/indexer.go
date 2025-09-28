package indexer

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	NameContains   string
	MinSize        int64
	MaxSize        int64
	ModifiedAfter  time.Time
	ModifiedBefore time.Time
}

// Indexer builds and maintains an in-memory representation of files on disk.
type Indexer struct {
	mu        sync.RWMutex
	files     map[string]FileRecord
	scanRoots []string
}

// New constructs an Indexer for the provided root directories.
func New(scanRoots []string) (*Indexer, error) {
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
	}, nil
}

// BuildInitialIndex walks through the configured roots and captures file metadata.
func (idx *Indexer) BuildInitialIndex(ctx context.Context) error {
	for _, root := range idx.scanRoots {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				// Continue walking on errors but capture the first one encountered.
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			record := FileRecord{
				Path:     filepath.Clean(path),
				Name:     info.Name(),
				Size:     info.Size(),
				ModTime:  info.ModTime(),
				RootPath: root,
			}
			idx.upsert(record)
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// Search returns a slice of FileRecord that match the query parameters.
func (idx *Indexer) Search(ctx context.Context, query Query) []FileRecord {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	matches := make([]FileRecord, 0)
	for _, record := range idx.files {
		if ctx.Err() != nil {
			break
		}
		if !matchesQuery(record, query) {
			continue
		}
		matches = append(matches, record)
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Name == matches[j].Name {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Name < matches[j].Name
	})
	return matches
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
	idx.upsert(record)
}

// RemoveFile removes a file from the index by its path.
func (idx *Indexer) RemoveFile(path string) {
	normalized := filepath.Clean(path)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.files, normalized)
}

// TODO: implement background filesystem watching to keep the index in sync.
// func (idx *Indexer) StartWatching(ctx context.Context) error {
//     return errors.New("watching not yet implemented")
// }

func (idx *Indexer) upsert(record FileRecord) {
	normalized := filepath.Clean(record.Path)
	record.Path = normalized

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.files[normalized] = record
}

func matchesQuery(record FileRecord, query Query) bool {
	if query.NameContains != "" {
		if !strings.Contains(strings.ToLower(record.Name), strings.ToLower(query.NameContains)) {
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
