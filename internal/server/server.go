package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"seekfile/internal/frontend"
	"seekfile/internal/indexer"
)

const (
	defaultPageSize = 20
	maxPageSize     = 200
)

var categoryExtensions = map[string][]string{
	"documents": {".txt", ".md", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".csv"},
	"images":    {".png", ".jpg", ".jpeg", ".gif", ".bmp", ".svg", ".webp", ".tiff"},
	"audio":     {".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".wma"},
	"video":     {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v"},
}

// Server wires together HTTP handlers for the API and embedded frontend.
type Server struct {
	index    *indexer.Indexer
	renderer *frontend.Renderer
	baseCtx  context.Context
}

// New creates a Server instance backed by the provided indexer and renderer.
func New(idx *indexer.Indexer, renderer *frontend.Renderer) *Server {
	return &Server{index: idx, renderer: renderer, baseCtx: context.Background()}
}

// Routes returns the HTTP handler that exposes the application endpoints.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.Handle("/static/", http.StripPrefix("/static/", s.renderer.StaticHandler()))
	return mux
}

// Start runs the HTTP server until the provided context is cancelled.
func (s *Server) Start(ctx context.Context, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.baseCtx = ctx

	srv := &http.Server{
		Addr:    addr,
		Handler: s.Routes(),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{
		"Year": time.Now().Year(),
	}
	if err := s.renderer.RenderIndex(w, data); err != nil {
		http.Error(w, fmt.Sprintf("render page: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	queryValues := r.URL.Query()
	idxQuery := indexer.Query{
		NamePattern: strings.TrimSpace(queryValues.Get("query")),
	}
	if minSizeStr := queryValues.Get("minSize"); minSizeStr != "" {
		if minSize, err := strconv.ParseInt(minSizeStr, 10, 64); err == nil {
			idxQuery.MinSize = minSize
		}
	}
	if maxSizeStr := queryValues.Get("maxSize"); maxSizeStr != "" {
		if maxSize, err := strconv.ParseInt(maxSizeStr, 10, 64); err == nil {
			idxQuery.MaxSize = maxSize
		}
	}

	categories := queryValues["category"]
	if exts := resolveCategoryExtensions(categories); len(exts) > 0 {
		idxQuery.Extensions = exts
	}

	sortField := strings.TrimSpace(queryValues.Get("sort"))
	if sortField != "" {
		idxQuery.SortField = sortField
	}
	idxQuery.SortDescending = strings.EqualFold(queryValues.Get("order"), "desc")

	page := parsePositiveInt(queryValues.Get("page"), 1)
	pageSize := clampPageSize(parsePositiveInt(queryValues.Get("pageSize"), defaultPageSize))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	idxQuery.Offset = offset
	idxQuery.Limit = pageSize

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result := s.index.Search(ctx, idxQuery)

	totalPages := 0
	if pageSize > 0 && result.Total > 0 {
		totalPages = (result.Total + pageSize - 1) / pageSize
	}

	if totalPages > 0 && page > totalPages {
		page = totalPages
		idxQuery.Offset = (page - 1) * pageSize
		result = s.index.Search(ctx, idxQuery)
	}

	sortFieldResponse := idxQuery.SortField
	if sortFieldResponse == "" {
		sortFieldResponse = "name"
	}

	response := map[string]any{
		"files":      result.Files,
		"total":      result.Total,
		"page":       page,
		"pageSize":   pageSize,
		"totalPages": totalPages,
		"sort":       sortFieldResponse,
		"order":      ternary(idxQuery.SortDescending, "desc", "asc"),
	}

	writeJSON(w, response)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	record, ok := s.index.Lookup(path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if !s.isWithinRoots(record.Path) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", record.Name))
	http.ServeFile(w, r, record.Path)
}

func (s *Server) isWithinRoots(path string) bool {
	for _, root := range s.index.Roots() {
		if isSubPath(root, path) {
			return true
		}
	}
	return false
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := s.index.Status()
	writeJSON(w, status)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Mode string `json:"mode"`
	}

	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
			return
		}
	}

	mode, err := indexer.ParseScanMode(payload.Mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.index.StartScan(s.baseCtx, mode); err != nil {
		if errors.Is(err, indexer.ErrScanInProgress) {
			http.Error(w, "scan already in progress", http.StatusConflict)
			return
		}
		http.Error(w, fmt.Sprintf("start scan: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"status": s.index.Status()})
}

func isSubPath(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
	}
}

func parsePositiveInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func clampPageSize(size int) int {
	if size <= 0 {
		return defaultPageSize
	}
	if size > maxPageSize {
		return maxPageSize
	}
	return size
}

func resolveCategoryExtensions(categories []string) []string {
	if len(categories) == 0 {
		return nil
	}
	extSet := make(map[string]struct{})
	for _, raw := range categories {
		category := strings.ToLower(strings.TrimSpace(raw))
		if category == "" || category == "all" {
			return nil
		}
		extensions, ok := categoryExtensions[category]
		if !ok {
			continue
		}
		for _, ext := range extensions {
			extSet[ext] = struct{}{}
		}
	}
	if len(extSet) == 0 {
		return nil
	}
	result := make([]string, 0, len(extSet))
	for ext := range extSet {
		result = append(result, ext)
	}
	sort.Strings(result)
	return result
}

func ternary(cond bool, trueVal, falseVal string) string {
	if cond {
		return trueVal
	}
	return falseVal
}
