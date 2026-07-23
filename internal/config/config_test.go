package config

import "testing"

func TestLoadRejectsPublicDemoWithoutAPIKey(t *testing.T) {
	t.Setenv("DOCWEAVE_PUBLIC_DEMO", "true")
	t.Setenv("DOCWEAVE_API_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing API key to fail")
	}
}
