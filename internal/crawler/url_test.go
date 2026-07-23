package crawler

import (
	"net"
	"net/url"
	"testing"
)

func TestCanonicalize(t *testing.T) {
	base, _ := url.Parse("https://EXAMPLE.com/docs/start/")
	got, err := Canonicalize("../api/?utm_source=test&b=2&a=1#top", base)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/docs/api/?a=1&b=2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalizeRejectsUnsafeSchemes(t *testing.T) {
	if _, err := Canonicalize("file:///etc/passwd", nil); err == nil {
		t.Fatal("expected file URL to be rejected")
	}
}

func TestPublicIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", false},
		{"10.0.0.5", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"8.8.8.8", true},
		{"::1", false},
		{"2606:4700:4700::1111", true},
	}
	for _, test := range tests {
		if got := publicIP(net.ParseIP(test.ip)); got != test.want {
			t.Errorf("publicIP(%s)=%v, want %v", test.ip, got, test.want)
		}
	}
}

func TestAllowedHostDoesNotMatchSubdomainsImplicitly(t *testing.T) {
	if AllowedHost("evil.example.com", []string{"example.com"}) {
		t.Fatal("subdomain must be explicitly allowed")
	}
}
