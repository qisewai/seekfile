package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"seekfile/internal/frontend"
	"seekfile/internal/indexer"
)

// Server wires together HTTP handlers for the API and embedded frontend.
type Server struct {
	index    *indexer.Indexer
	renderer *frontend.Renderer
}

// New creates a Server instance backed by the provided indexer and renderer.
func New(idx *indexer.Indexer, renderer *frontend.Renderer) *Server {
	return &Server{index: idx, renderer: renderer}
}

// Routes returns the HTTP handler that exposes the application endpoints.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.Handle("/static/", http.StripPrefix("/static/", s.renderer.StaticHandler()))
	return mux
}

// Start runs the HTTP server until the provided context is cancelled.
func (s *Server) Start(ctx context.Context, addr string) error {
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
		NameContains: queryValues.Get("query"),
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	files := s.index.Search(ctx, idxQuery)
	writeJSON(w, map[string]any{"files": files})
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
