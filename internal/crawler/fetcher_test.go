package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetcherRobotsAndBodyLimit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: DocWeave\nDisallow: /private\n")
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><title>ok</title></html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher("DocWeave/1.0", 1024, true)
	fetcher.client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true // fixture certificate only
	allowed, _, err := fetcher.AllowedByRobots(context.Background(), server.URL+"/page")
	if err != nil || !allowed {
		t.Fatalf("expected page to be allowed: allowed=%v err=%v", allowed, err)
	}
	allowed, _, err = fetcher.AllowedByRobots(context.Background(), server.URL+"/private")
	if err != nil || allowed {
		t.Fatalf("expected private path to be disallowed: allowed=%v err=%v", allowed, err)
	}
	result, err := fetcher.Fetch(context.Background(), server.URL+"/page", "", "")
	if err != nil || result.StatusCode != http.StatusOK {
		t.Fatalf("unexpected fetch result: %#v err=%v", result, err)
	}
}
