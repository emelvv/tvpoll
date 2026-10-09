package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr, ControlURL, AdminToken, CookieSecret, DedupSecret, PublicOrigin string
	ShardURLs                                                             []string
	CookieSecure, DemoMode                                                bool
	BatchSize, Workers, QueueSize, DBConns, MaxInFlight                   int
	BatchWait, DBTimeout                                                  time.Duration
}

func Load() (Config, error) {
	c := Config{Addr: env("HTTP_ADDR", ":8080"), ControlURL: os.Getenv("CONTROL_DATABASE_URL"),
		AdminToken: os.Getenv("ADMIN_TOKEN"), CookieSecret: os.Getenv("COOKIE_SECRET"),
		DedupSecret: os.Getenv("DEDUP_SECRET"), PublicOrigin: env("PUBLIC_ORIGIN", "http://localhost:8080")}
	// Docker hosts commonly supply PORT; an explicit HTTP_ADDR takes priority.
	if os.Getenv("HTTP_ADDR") == "" {
		port := env("PORT", "8080")
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("PORT must be in [1,65535]")
		}
		c.Addr = ":" + port
	}
	for _, v := range strings.Split(os.Getenv("SHARD_DATABASE_URLS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			c.ShardURLs = append(c.ShardURLs, v)
		}
	}
	if c.ControlURL == "" || len(c.ShardURLs) == 0 {
		return c, fmt.Errorf("CONTROL_DATABASE_URL and SHARD_DATABASE_URLS are required")
	}
	seen := map[string]bool{}
	for _, v := range c.ShardURLs {
		if seen[v] {
			return c, fmt.Errorf("duplicate shard URL")
		}
		seen[v] = true
	}
	for name, secret := range map[string]string{"ADMIN_TOKEN": c.AdminToken, "COOKIE_SECRET": c.CookieSecret, "DEDUP_SECRET": c.DedupSecret} {
		if len(secret) < 32 {
			return c, fmt.Errorf("%s must contain at least 32 bytes", name)
		}
	}
	if c.CookieSecret == c.DedupSecret || c.CookieSecret == c.AdminToken || c.DedupSecret == c.AdminToken {
		return c, fmt.Errorf("secrets must be distinct")
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return c, fmt.Errorf("PUBLIC_ORIGIN must be an http(s) origin without a path")
	}
	c.CookieSecure, err = strconv.ParseBool(env("COOKIE_SECURE", "true"))
	if err != nil {
		return c, fmt.Errorf("COOKIE_SECURE must be true or false")
	}
	if u.Scheme == "https" && !c.CookieSecure {
		return c, fmt.Errorf("https PUBLIC_ORIGIN requires COOKIE_SECURE=true")
	}
	if u.Scheme == "http" && c.CookieSecure {
		return c, fmt.Errorf("http PUBLIC_ORIGIN requires explicit COOKIE_SECURE=false")
	}
	c.DemoMode, err = strconv.ParseBool(env("DEMO_MODE", "false"))
	if err != nil {
		return c, fmt.Errorf("DEMO_MODE must be true or false")
	}
	fields := []struct {
		name          string
		dst           *int
		def, min, max int
	}{
		{"BATCH_SIZE", &c.BatchSize, 256, 1, 4096}, {"WORKERS_PER_SHARD", &c.Workers, 4, 1, 64},
		{"QUEUE_SIZE", &c.QueueSize, 4096, 1, 100000}, {"MAX_DB_CONNS", &c.DBConns, 8, 1, 128},
		{"MAX_IN_FLIGHT", &c.MaxInFlight, 8192, 1, 1000000},
	}
	for _, f := range fields {
		n, e := strconv.Atoi(env(f.name, strconv.Itoa(f.def)))
		if e != nil || n < f.min || n > f.max {
			return c, fmt.Errorf("%s must be in [%d,%d]", f.name, f.min, f.max)
		}
		*f.dst = n
	}
	if c.DBConns < c.Workers {
		return c, fmt.Errorf("MAX_DB_CONNS must be >= WORKERS_PER_SHARD")
	}
	for _, f := range []struct {
		name          string
		dst           *time.Duration
		def, min, max int
	}{
		{"BATCH_WAIT_MS", &c.BatchWait, 5, 1, 1000}, {"DB_TIMEOUT_MS", &c.DBTimeout, 2000, 10, 30000},
	} {
		n, e := strconv.Atoi(env(f.name, strconv.Itoa(f.def)))
		if e != nil || n < f.min || n > f.max {
			return c, fmt.Errorf("%s out of range", f.name)
		}
		*f.dst = time.Duration(n) * time.Millisecond
	}
	return c, nil
}

func env(k, fallback string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return fallback
}
