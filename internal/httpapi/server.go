package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bananatruck/docweave/internal/config"
	"github.com/bananatruck/docweave/internal/crawler"
	"github.com/bananatruck/docweave/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type crawlStore interface {
	CreateCrawl(ctx context.Context, seeds, hosts []string, maxDepth, maxPages, delayMS int) (string, error)
	GetCrawl(ctx context.Context, id string) (store.Crawl, error)
	ListCrawls(ctx context.Context, limit int) ([]store.Crawl, error)
	ListPages(ctx context.Context, crawlID, query string, limit int) ([]store.Page, error)
	ListChanges(ctx context.Context, crawlID string) ([]store.Change, error)
	GetVersions(ctx context.Context, crawlID, pageID string) ([]store.Version, error)
	Recrawl(ctx context.Context, crawlID string) error
	Ping(ctx context.Context) error
}

type Server struct {
	store  crawlStore
	cfg    config.Config
	logger *slog.Logger
}

type createCrawlRequest struct {
	SeedURLs     []string `json:"seed_urls"`
	AllowedHosts []string `json:"allowed_hosts"`
	MaxDepth     int      `json:"max_depth"`
	MaxPages     int      `json:"max_pages"`
	CrawlDelayMS int      `json:"crawl_delay_ms"`
}

func New(store crawlStore, cfg config.Config, logger *slog.Logger) http.Handler {
	server := &Server{store: store, cfg: cfg, logger: logger}
	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	router.Use(server.accessLog)
	router.Get("/", server.dashboard)
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", server.ready)
	router.Handle("/metrics", promhttp.Handler())
	router.Route("/api/v1", func(api chi.Router) {
		api.Get("/crawls", server.listCrawls)
		api.With(server.authorize).Post("/crawls", server.createCrawl)
		api.Route("/crawls/{crawlID}", func(crawl chi.Router) {
			crawl.Get("/", server.getCrawl)
			crawl.Get("/events", server.events)
			crawl.Get("/pages", server.listPages)
			crawl.Get("/changes", server.listChanges)
			crawl.Get("/pages/{pageID}/versions", server.getVersions)
			crawl.With(server.authorize).Post("/recrawl", server.recrawl)
		})
	})
	return router
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) createCrawl(w http.ResponseWriter, r *http.Request) {
	var request createCrawlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	seeds, hosts, err := s.validateRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.MaxDepth == 0 {
		request.MaxDepth = 2
	}
	if request.MaxPages == 0 {
		request.MaxPages = 200
	}
	if request.CrawlDelayMS == 0 {
		request.CrawlDelayMS = 500
	}
	if s.cfg.PublicDemo && request.MaxPages > 250 {
		request.MaxPages = 250
	}
	if request.MaxDepth < 0 || request.MaxDepth > 10 || request.MaxPages < 1 || request.MaxPages > 100000 ||
		request.CrawlDelayMS < 100 || request.CrawlDelayMS > 60000 {
		writeError(w, http.StatusBadRequest, "limits are outside the supported range")
		return
	}
	id, err := s.store.CreateCrawl(r.Context(), seeds, hosts, request.MaxDepth, request.MaxPages, request.CrawlDelayMS)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/crawls/"+id)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "queued"})
}

func (s *Server) validateRequest(request createCrawlRequest) ([]string, []string, error) {
	if len(request.SeedURLs) < 1 || len(request.SeedURLs) > 10 {
		return nil, nil, errors.New("provide between 1 and 10 seed URLs")
	}
	hostSet := make(map[string]struct{})
	for _, host := range request.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			hostSet[host] = struct{}{}
		}
	}
	seeds := make([]string, 0, len(request.SeedURLs))
	for _, seed := range request.SeedURLs {
		normalized, err := crawler.Canonicalize(seed, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid seed URL: %w", err)
		}
		parsed, _ := url.Parse(normalized)
		host := strings.ToLower(parsed.Hostname())
		if len(hostSet) == 0 {
			hostSet[host] = struct{}{}
		}
		if _, ok := hostSet[host]; !ok {
			return nil, nil, fmt.Errorf("seed host %q is not allowed", host)
		}
		if s.cfg.PublicDemo {
			if _, ok := s.cfg.DemoHosts[host]; !ok {
				return nil, nil, fmt.Errorf("host %q is unavailable in the public demo", host)
			}
		}
		seeds = append(seeds, normalized)
	}
	hosts := make([]string, 0, len(hostSet))
	for host := range hostSet {
		if parsed := netURLForHost(host); parsed == nil {
			return nil, nil, fmt.Errorf("invalid allowed host %q", host)
		}
		if s.cfg.PublicDemo {
			if _, ok := s.cfg.DemoHosts[host]; !ok {
				return nil, nil, fmt.Errorf("host %q is unavailable in the public demo", host)
			}
		}
		hosts = append(hosts, host)
	}
	return seeds, hosts, nil
}

func netURLForHost(host string) *url.URL {
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.Hostname() != host || parsed.Port() != "" || parsed.User != nil {
		return nil
	}
	return parsed
}

func (s *Server) listCrawls(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListCrawls(r.Context(), 50)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"crawls": items})
}

func (s *Server) getCrawl(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetCrawl(r.Context(), chi.URLParam(r, "crawlID"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listPages(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListPages(r.Context(), chi.URLParam(r, "crawlID"), r.URL.Query().Get("q"), 100)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": items})
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListChanges(r.Context(), chi.URLParam(r, "crawlID"))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": items})
}

func (s *Server) getVersions(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.GetVersions(r.Context(), chi.URLParam(r, "crawlID"), chi.URLParam(r, "pageID"))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": items})
}

func (s *Server) recrawl(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Recrawl(r.Context(), chi.URLParam(r, "crawlID")); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		item, err := s.store.GetCrawl(r.Context(), chi.URLParam(r, "crawlID"))
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
			flusher.Flush()
			return
		}
		data, _ := json.Marshal(item)
		fmt.Fprintf(w, "event: crawl\ndata: %s\n\n", data)
		flusher.Flush()
		if item.Status == "completed" || item.Status == "cancelled" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" && !s.cfg.PublicDemo {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-API-Key")
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "valid API key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		s.logger.Info("http request", "request_id", middleware.GetReqID(r.Context()), "method", r.Method,
			"path", r.URL.Path, "status", wrapped.Status(), "bytes", wrapped.BytesWritten(),
			"duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	s.internalError(w, err)
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
