package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bananatruck/docweave/internal/config"
	"github.com/bananatruck/docweave/internal/store"
	"github.com/jackc/pgx/v5"
)

type fakeStore struct {
	mu     sync.Mutex
	crawls map[string]store.Crawl
	seq    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{crawls: map[string]store.Crawl{}}
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func (f *fakeStore) CreateCrawl(_ context.Context, seeds, hosts []string, maxDepth, maxPages, delayMS int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("c%d", f.seq)
	f.crawls[id] = store.Crawl{
		ID: id, Status: "queued", SeedURLs: seeds, AllowedHosts: hosts,
		MaxDepth: maxDepth, MaxPages: maxPages, CrawlDelayMS: delayMS,
	}
	return id, nil
}

func (f *fakeStore) GetCrawl(_ context.Context, id string) (store.Crawl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.crawls[id]
	if !ok {
		return store.Crawl{}, pgx.ErrNoRows
	}
	return item, nil
}

func (f *fakeStore) complete(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := f.crawls[id]
	item.Status = "completed"
	f.crawls[id] = item
}

func (f *fakeStore) ListCrawls(context.Context, int) ([]store.Crawl, error) { return nil, nil }
func (f *fakeStore) ListPages(context.Context, string, string, int) ([]store.Page, error) {
	return nil, nil
}
func (f *fakeStore) ListChanges(context.Context, string) ([]store.Change, error) { return nil, nil }
func (f *fakeStore) GetVersions(context.Context, string, string) ([]store.Version, error) {
	return nil, nil
}
func (f *fakeStore) Recrawl(context.Context, string) error { return nil }

func testHandler(t *testing.T, backend crawlStore, cfg config.Config) http.Handler {
	t.Helper()
	if cfg.UserAgent == "" {
		cfg.UserAgent = "DocWeave-test"
	}
	return New(backend, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestHealthz(t *testing.T) {
	handler := testHandler(t, newFakeStore(), config.Config{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCreateCrawlRequiresAPIKey(t *testing.T) {
	handler := testHandler(t, newFakeStore(), config.Config{APIKey: "secret"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crawls", strings.NewReader(`{"seed_urls":["https://go.dev"]}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestCreateCrawlRejectsInvalidJSON(t *testing.T) {
	handler := testHandler(t, newFakeStore(), config.Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crawls", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestCreateCrawlAndGetStatus(t *testing.T) {
	backend := newFakeStore()
	handler := testHandler(t, backend, config.Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crawls", strings.NewReader(`{"seed_urls":["https://go.dev/doc"]}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d, body %s", rec.Code, rec.Body.String())
	}
	var created map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]
	if id == "" {
		t.Fatal("missing id")
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/crawls/"+id {
		t.Fatalf("Location %q", loc)
	}

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/crawls/"+id, nil))
	if status.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", status.Code, status.Body.String())
	}
	var crawl store.Crawl
	if err := json.Unmarshal(status.Body.Bytes(), &crawl); err != nil {
		t.Fatal(err)
	}
	if crawl.Status != "queued" || crawl.ID != id {
		t.Fatalf("crawl %+v", crawl)
	}
}

func TestEventsStreamCompletes(t *testing.T) {
	backend := newFakeStore()
	id, err := backend.CreateCrawl(context.Background(), []string{"https://go.dev"}, []string{"go.dev"}, 2, 10, 500)
	if err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t, backend, config.Config{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crawls/"+id+"/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	backend.complete(id)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not return after crawl completed")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: crawl") {
		t.Fatalf("missing crawl event: %q", body)
	}
	if !strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("missing completed status: %q", body)
	}
}
