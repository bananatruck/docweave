package crawler

import (
	"strings"
	"testing"
)

func TestParseHTML(t *testing.T) {
	html := `<html><head><title> API Guide </title><link rel="canonical" href="/guide"></head>
	<body><nav>Menu</nav><main><h1>Start</h1><p>Hello   world.</p>
	<a href="/next?utm_source=x">Next</a><a href="https://outside.test">Outside</a></main>
	<script>ignored()</script></body></html>`
	doc, err := ParseHTML([]byte(html), "https://docs.example.test/start", []string{"docs.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "API Guide" || doc.Text != "Start Hello world. Next Outside" {
		t.Fatalf("unexpected document: %#v", doc)
	}
	if doc.CanonicalURL != "https://docs.example.test/guide" {
		t.Fatalf("unexpected canonical URL %q", doc.CanonicalURL)
	}
	if len(doc.Links) != 1 || doc.Links[0] != "https://docs.example.test/next" {
		t.Fatalf("unexpected links: %#v", doc.Links)
	}
	if len(doc.ContentHash) != 64 {
		t.Fatalf("unexpected hash %q", doc.ContentHash)
	}
}

func BenchmarkParseHTML(b *testing.B) {
	body := []byte(`<html><head><title>Reference</title></head><body><main>` +
		strings.Repeat(`<section><h2>Function</h2><p>Technical documentation content.</p><a href="/next">Next</a></section>`, 200) +
		`</main></body></html>`)
	for b.Loop() {
		if _, err := ParseHTML(body, "https://docs.example.test/start", []string{"docs.example.test"}); err != nil {
			b.Fatal(err)
		}
	}
}
