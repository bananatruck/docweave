package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bananatruck/docweave/internal/observability"
	"github.com/bananatruck/docweave/internal/store"
)

type Worker struct {
	ID       string
	Store    *store.Store
	Fetcher  *Fetcher
	Metrics  *observability.Metrics
	LeaseTTL time.Duration
	Logger   *slog.Logger
}

func (w *Worker) Run(ctx context.Context) error {
	w.Logger.Info("worker started", "worker_id", w.ID)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		work, err := w.Store.Lease(ctx, w.ID, w.LeaseTTL)
		if err != nil {
			w.Logger.Error("lease failed", "error", err)
			if !wait(ctx, time.Second) {
				return nil
			}
			continue
		}
		if work == nil {
			if !wait(ctx, 300*time.Millisecond) {
				return nil
			}
			continue
		}
		w.process(ctx, work)
	}
}

func (w *Worker) process(ctx context.Context, work *store.Work) {
	started := time.Now()
	w.Metrics.Active.Inc()
	defer w.Metrics.Active.Dec()
	logger := w.Logger.With("worker_id", w.ID, "crawl_id", work.CrawlID, "url", work.URL)
	parsed, err := url.Parse(work.URL)
	if err != nil || !AllowedHost(parsed.Hostname(), work.AllowedHosts) {
		w.fail(ctx, work, fmt.Errorf("URL escaped allowed hosts"), started, 0, false)
		return
	}
	allowed, robotsDelay, err := w.Fetcher.AllowedByRobots(ctx, work.URL)
	if err != nil {
		w.fail(ctx, work, fmt.Errorf("robots.txt: %w", err), started, 0, true)
		return
	}
	if !allowed {
		duration := time.Since(started)
		if err := w.Store.Skip(ctx, work, "disallowed by robots.txt", duration, w.ID); err != nil {
			logger.Error("persist skipped URL", "error", err)
			return
		}
		w.observe("skipped", 0, duration)
		return
	}
	delay := work.CrawlDelay
	if robotsDelay > delay {
		delay = robotsDelay
	}
	hostWait, err := w.Store.ReserveHost(ctx, parsed.Hostname(), delay)
	if err != nil {
		w.fail(ctx, work, fmt.Errorf("reserve host: %w", err), started, 0, true)
		return
	}
	if !wait(ctx, hostWait) {
		return
	}
	etag, modified := w.Store.PreviousHeaders(ctx, work.CrawlID, work.URLHash)
	result, err := w.Fetcher.Fetch(ctx, work.URL, etag, modified)
	if err != nil {
		w.fail(ctx, work, err, started, result.StatusCode, retryableStatus(result.StatusCode))
		return
	}
	if result.StatusCode == http.StatusNotModified {
		duration := time.Since(started)
		if err := w.Store.NotModified(ctx, work, result.StatusCode, duration, w.ID); err != nil {
			logger.Error("store not-modified result", "error", err)
			return
		}
		w.observe("not_modified", result.StatusCode, duration)
		return
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		w.fail(ctx, work, fmt.Errorf("unexpected HTTP status %d", result.StatusCode), started, result.StatusCode, retryableStatus(result.StatusCode))
		return
	}
	document, err := ParseHTML(result.Body, result.FinalURL, work.AllowedHosts)
	if err != nil {
		w.fail(ctx, work, fmt.Errorf("parse document: %w", err), started, result.StatusCode, false)
		return
	}
	duration := time.Since(started)
	page := store.PageInput{
		URL: work.URL, URLHash: work.URLHash, CanonicalURL: document.CanonicalURL,
		Title: document.Title, Text: document.Text, ContentHash: document.ContentHash,
		ETag: result.ETag, LastModified: result.LastModified, StatusCode: result.StatusCode,
	}
	if err := w.Store.Complete(ctx, work, page, document.Links, duration, w.ID); err != nil {
		logger.Error("persist page", "error", err)
		return
	}
	w.Metrics.Discovered.Add(float64(len(document.Links)))
	w.observe("success", result.StatusCode, duration)
	logger.Info("page crawled", "status", result.StatusCode, "links", len(document.Links), "duration", duration)
}

func (w *Worker) fail(ctx context.Context, work *store.Work, cause error, started time.Time, status int, retryable bool) {
	duration := time.Since(started)
	if err := w.Store.Fail(ctx, work, cause, duration, w.ID, status, retryable); err != nil {
		w.Logger.Error("persist failure", "error", err, "cause", cause)
	}
	w.observe("error", status, duration)
	w.Logger.Warn("fetch failed", "crawl_id", work.CrawlID, "url", work.URL, "error", cause, "attempt", work.Attempts)
}

func retryableStatus(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= 500
}

func (w *Worker) observe(outcome string, status int, duration time.Duration) {
	w.Metrics.Fetches.WithLabelValues(outcome, strconv.Itoa(status)).Inc()
	w.Metrics.Duration.WithLabelValues(outcome).Observe(duration.Seconds())
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
