package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/snat121/PiSIEM/internal/storage"
)

type Server struct {
	store     *storage.Store
	logger    *slog.Logger
	templates *template.Template
	start     time.Time
	addr      string
	httpSrv   *http.Server
}

func NewServer(addr string, store *storage.Store, uiFS fs.FS, logger *slog.Logger) (*Server, error) {
	tpls, err := template.ParseFS(uiFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		addr:      addr,
		store:     store,
		logger:    logger,
		templates: tpls,
		start:     time.Now(),
	}, nil
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/flush", s.handleFlush)

	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("http server stopped", "err", err)
		}
	}()
	s.logger.Info("web server listening", "addr", s.addr)
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "index.html", nil); err != nil {
		s.logger.Error("render index", "err", err)
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.LogFilter{
		Host:       q.Get("host"),
		Source:     q.Get("source"),
		SourceFile: q.Get("source_file"),
		Query:      q.Get("q"),
	}
	if v := q.Get("severity"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Severity = &n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}

	logs, total, err := s.store.QueryLogs(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if isHTMXRequest(r) {
		s.renderLogRows(w, logs)
		return
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if logs == nil {
		logs = []storage.LogEvent{}
	}
	writeJSON(w, map[string]any{
		"total":  total,
		"limit":  limit,
		"offset": f.Offset,
		"logs":   logs,
	})
}

func (s *Server) renderLogRows(w http.ResponseWriter, logs []storage.LogEvent) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(logs) == 0 {
		fmt.Fprint(w, `<tr><td colspan="6" class="px-2 py-4 text-gray-500 text-center">no logs</td></tr>`)
		return
	}
	for _, l := range logs {
		fmt.Fprintf(w,
			`<tr class="border-b border-gray-800 hover:bg-gray-900">`+
				`<td class="px-2 py-1 text-gray-400 whitespace-nowrap">%s</td>`+
				`<td class="px-2 py-1 whitespace-nowrap">%s</td>`+
				`<td class="px-2 py-1">%d</td>`+
				`<td class="px-2 py-1">%s</td>`+
				`<td class="px-2 py-1 text-gray-400">%s</td>`+
				`<td class="px-2 py-1 font-mono">%s</td>`+
				`</tr>`,
			template.HTMLEscapeString(l.Timestamp.Format(time.RFC3339)),
			template.HTMLEscapeString(l.Host),
			l.Severity,
			template.HTMLEscapeString(l.Source),
			template.HTMLEscapeString(l.SourceFile),
			template.HTMLEscapeString(l.Message),
		)
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if isHTMXRequest(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		fmt.Fprintf(&b, `<div class="bg-gray-900 rounded p-3 border border-gray-800"><div class="text-xs text-gray-500">total events</div><div class="text-xl font-semibold">%d</div></div>`, stats.TotalEvents)
		fmt.Fprintf(&b, `<div class="bg-gray-900 rounded p-3 border border-gray-800"><div class="text-xs text-gray-500">events / minute</div><div class="text-xl font-semibold">%d</div></div>`, stats.EventsLastMinute)
		b.WriteString(`<div class="bg-gray-900 rounded p-3 border border-gray-800"><div class="text-xs text-gray-500 mb-1">top hosts</div>`)
		if len(stats.TopHosts) == 0 {
			b.WriteString(`<div class="text-xs text-gray-600">(none yet)</div>`)
		}
		for _, h := range stats.TopHosts {
			fmt.Fprintf(&b, `<div class="text-sm flex justify-between"><span>%s</span><span class="text-gray-500">%d</span></div>`, template.HTMLEscapeString(h.Host), h.Count)
		}
		b.WriteString(`</div>`)
		w.Write([]byte(b.String()))
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := map[string]any{
		"uptime_seconds": int(time.Since(s.start).Seconds()),
		"buffer_depth":   s.store.BufferDepth(),
		"dropped_events": s.store.DroppedEvents(),
		"total_written":  s.store.TotalWritten(),
		"db_size_bytes":  s.store.DBSize(),
		"goroutines":     runtime.NumGoroutine(),
	}
	if isHTMXRequest(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w,
			`uptime %ds · buffer %d · dropped %d · goroutines %d`,
			health["uptime_seconds"], health["buffer_depth"], health["dropped_events"], health["goroutines"],
		)
		return
	}
	writeJSON(w, health)
}

// handleFlush forces the storage writer to drain the in-memory buffer to
// SQLite immediately. Useful so a dashboard "Refresh" can show events that
// would otherwise sit in the buffer until the next 60s tick.
func (s *Server) handleFlush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.store.FlushNow()
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
