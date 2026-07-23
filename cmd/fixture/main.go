package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func main() {
	addr := getenv("FIXTURE_ADDR", ":8081")
	pages := envInt("FIXTURE_PAGES", 1000)
	version := getenv("FIXTURE_VERSION", "v1")
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nAllow: /\n")
	})
	mux.HandleFunc("/page/", func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/page/"))
		if err != nil || index < 0 || index >= pages {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", fmt.Sprintf(`"%s-%d"`, version, index))
		if r.Header.Get("If-None-Match") == fmt.Sprintf(`"%s-%d"`, version, index) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprintf(w, "<!doctype html><title>Fixture page %d</title><main><h1>Page %d</h1><p>Deterministic benchmark content %s.</p>", index, index, version)
		for child := index*10 + 1; child <= index*10+10 && child < pages; child++ {
			fmt.Fprintf(w, `<a href="/page/%d">Page %d</a>`, child, child)
		}
		fmt.Fprint(w, "</main>")
	})
	log.Printf("fixture listening on %s with %d pages", addr, pages)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
