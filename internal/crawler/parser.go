package crawler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

var whitespace = regexp.MustCompile(`\s+`)

type Document struct {
	Title, Text, ContentHash, CanonicalURL string
	Links                                  []string
}

func ParseHTML(body []byte, finalURL string, allowedHosts []string) (Document, error) {
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return Document{}, err
	}
	base, err := url.Parse(finalURL)
	if err != nil {
		return Document{}, err
	}
	document.Find("script,style,noscript,svg,canvas").Remove()
	title := strings.TrimSpace(document.Find("title").First().Text())
	canonical := ""
	if href, ok := document.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		if normalized, err := Canonicalize(href, base); err == nil {
			canonical = normalized
		}
	}
	root := document.Find("main,article").First()
	if root.Length() == 0 {
		root = document.Find("body").First()
	}
	var textBuilder strings.Builder
	for _, node := range root.Nodes {
		collectText(node, &textBuilder)
	}
	text := whitespace.ReplaceAllString(strings.TrimSpace(textBuilder.String()), " ")
	if len(text) > 1_000_000 {
		text = text[:1_000_000]
	}
	sum := sha256.Sum256([]byte(title + "\n" + text))
	seen := make(map[string]struct{})
	links := make([]string, 0, 64)
	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, _ := selection.Attr("href")
		normalized, normalizeErr := Canonicalize(href, base)
		if normalizeErr != nil {
			return
		}
		target, parseErr := url.Parse(normalized)
		if parseErr != nil || !AllowedHost(target.Hostname(), allowedHosts) {
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		links = append(links, normalized)
	})
	return Document{
		Title: title, Text: text, ContentHash: hex.EncodeToString(sum[:]),
		CanonicalURL: canonical, Links: links,
	}, nil
}

func collectText(node *html.Node, builder *strings.Builder) {
	if node.Type == html.TextNode {
		builder.WriteString(node.Data)
		builder.WriteByte(' ')
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectText(child, builder)
	}
}
