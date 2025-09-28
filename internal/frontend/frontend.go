package frontend

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"sync"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Renderer encapsulates template rendering for the web UI.
type Renderer struct {
	once     sync.Once
	initErr  error
	template *template.Template
}

// NewRenderer creates a Renderer capable of serving the embedded UI.
func NewRenderer() *Renderer {
	return &Renderer{}
}

func (r *Renderer) ensureTemplates() error {
	r.once.Do(func() {
		tpl, err := template.ParseFS(assets, "templates/index.html")
		if err != nil {
			r.initErr = err
			return
		}
		r.template = tpl
	})
	return r.initErr
}

// RenderIndex writes the main HTML page to the response writer.
func (r *Renderer) RenderIndex(w http.ResponseWriter, data any) error {
	if err := r.ensureTemplates(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return r.template.ExecuteTemplate(w, "index.html", data)
}

// StaticHandler returns an http.Handler that serves embedded static assets.
func (r *Renderer) StaticHandler() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.NotFound(w, req)
		})
	}
	return http.FileServer(http.FS(sub))
}
