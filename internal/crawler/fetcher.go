package crawler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

type Result struct {
	StatusCode   int
	Body         []byte
	ContentType  string
	ETag         string
	LastModified string
	FinalURL     string
}

type robotsEntry struct {
	data    *robotstxt.RobotsData
	expires time.Time
}

type Fetcher struct {
	client       *http.Client
	resolver     *net.Resolver
	userAgent    string
	maxBody      int64
	allowPrivate bool
	mu           sync.Mutex
	robots       map[string]robotsEntry
}

func NewFetcher(userAgent string, maxBody int64, allowPrivate bool) *Fetcher {
	f := &Fetcher{
		resolver:     net.DefaultResolver,
		userAgent:    userAgent,
		maxBody:      maxBody,
		allowPrivate: allowPrivate,
		robots:       make(map[string]robotsEntry),
	}
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
		DialContext:           f.safeDial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return errors.New("too many redirects")
			}
			return validateURLShape(req.URL, allowPrivate)
		},
	}
	return f
}

func validateURLShape(parsed *url.URL, allowPrivate bool) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("unsupported URL scheme")
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return errors.New("unsafe URL")
	}
	port := parsed.Port()
	if !allowPrivate && port != "" && port != "80" && port != "443" {
		return errors.New("only ports 80 and 443 are allowed")
	}
	return nil
}

func (f *Fetcher) Validate(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if err := validateURLShape(parsed, f.allowPrivate); err != nil {
		return err
	}
	addresses, err := f.resolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("host has no IP addresses")
	}
	for _, address := range addresses {
		if !f.allowPrivate && !publicIP(address.IP) {
			return fmt.Errorf("host resolves to a non-public address: %s", address.IP)
		}
	}
	return nil
}

func (f *Fetcher) safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := f.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, candidate := range addresses {
		if !f.allowPrivate && !publicIP(candidate.IP) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("host has no safe public address")
}

func (f *Fetcher) AllowedByRobots(ctx context.Context, target string) (bool, time.Duration, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return false, 0, err
	}
	key := parsed.Scheme + "://" + parsed.Host
	f.mu.Lock()
	entry, ok := f.robots[key]
	f.mu.Unlock()
	if !ok || time.Now().After(entry.expires) {
		robotsURL := key + "/robots.txt"
		result, fetchErr := f.fetch(ctx, robotsURL, "", "", 1<<20, false)
		if fetchErr != nil {
			return false, 0, fmt.Errorf("fetch robots.txt: %w", fetchErr)
		}
		if result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden {
			data, _ := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /\n"))
			entry = robotsEntry{data: data, expires: time.Now().Add(15 * time.Minute)}
		} else if result.StatusCode >= 500 {
			return false, 0, fmt.Errorf("robots.txt returned status %d", result.StatusCode)
		} else if result.StatusCode >= 400 {
			entry = robotsEntry{data: nil, expires: time.Now().Add(15 * time.Minute)}
		} else {
			data, parseErr := robotstxt.FromBytes(result.Body)
			if parseErr != nil {
				return false, 0, parseErr
			}
			entry = robotsEntry{data: data, expires: time.Now().Add(time.Hour)}
		}
		f.mu.Lock()
		f.robots[key] = entry
		f.mu.Unlock()
	}
	if entry.data == nil {
		return true, 0, nil
	}
	product := strings.Fields(f.userAgent)[0]
	if slash := strings.IndexByte(product, '/'); slash >= 0 {
		product = product[:slash]
	}
	group := entry.data.FindGroup(product)
	delay := group.CrawlDelay
	return group.Test(parsed.RequestURI()), delay, nil
}

func (f *Fetcher) Fetch(ctx context.Context, target, etag, modified string) (Result, error) {
	return f.fetch(ctx, target, etag, modified, f.maxBody, true)
}

func (f *Fetcher) fetch(ctx context.Context, target, etag, modified string, maxBody int64, requireHTML bool) (Result, error) {
	if err := f.Validate(ctx, target); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	response, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	contentLength, _ := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if contentLength > maxBody {
		return Result{StatusCode: response.StatusCode}, fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return Result{StatusCode: response.StatusCode}, err
	}
	if int64(len(body)) > maxBody {
		return Result{StatusCode: response.StatusCode}, fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if requireHTML && response.StatusCode != http.StatusNotModified && !strings.Contains(contentType, "text/html") &&
		!strings.Contains(contentType, "application/xhtml+xml") {
		return Result{StatusCode: response.StatusCode}, fmt.Errorf("unsupported content type %q", contentType)
	}
	return Result{
		StatusCode: response.StatusCode, Body: body, ContentType: contentType,
		ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified"),
		FinalURL: response.Request.URL.String(),
	}, nil
}
