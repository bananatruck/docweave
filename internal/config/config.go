package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL  string
	Role         string
	Addr         string
	Workers      int
	APIKey       string
	PublicDemo   bool
	AllowPrivate bool
	DemoHosts    map[string]struct{}
	UserAgent    string
	LeaseTTL     time.Duration
	MaxBody      int64
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:  getenv("DOCWEAVE_DATABASE_URL", "postgres://docweave:docweave@localhost:5432/docweave?sslmode=disable"),
		Role:         strings.ToLower(getenv("DOCWEAVE_ROLE", "all")),
		Addr:         getenv("DOCWEAVE_ADDR", ":8080"),
		Workers:      envInt("DOCWEAVE_WORKERS", 4),
		APIKey:       os.Getenv("DOCWEAVE_API_KEY"),
		PublicDemo:   envBool("DOCWEAVE_PUBLIC_DEMO", false),
		AllowPrivate: envBool("DOCWEAVE_ALLOW_PRIVATE_NETWORKS", false),
		DemoHosts:    splitSet(getenv("DOCWEAVE_DEMO_HOSTS", "go.dev,pkg.go.dev")),
		UserAgent:    getenv("DOCWEAVE_USER_AGENT", "DocWeave/1.0 (+https://github.com/bananatruck/docweave)"),
		LeaseTTL:     time.Duration(envInt("DOCWEAVE_LEASE_SECONDS", 45)) * time.Second,
		MaxBody:      int64(envInt("DOCWEAVE_MAX_BODY_MB", 5)) << 20,
	}
	if cfg.Role != "api" && cfg.Role != "worker" && cfg.Role != "all" {
		return Config{}, fmt.Errorf("DOCWEAVE_ROLE must be api, worker, or all")
	}
	if cfg.Workers < 1 || cfg.Workers > 64 {
		return Config{}, fmt.Errorf("DOCWEAVE_WORKERS must be between 1 and 64")
	}
	if cfg.PublicDemo && cfg.APIKey == "" {
		return Config{}, fmt.Errorf("DOCWEAVE_API_KEY is required in public demo mode")
	}
	if cfg.PublicDemo && cfg.AllowPrivate {
		return Config{}, fmt.Errorf("private-network crawling cannot be enabled in public demo mode")
	}
	return cfg, nil
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

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			result[item] = struct{}{}
		}
	}
	return result
}
