package config

import (
	"strings"
	"testing"
)

func base(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{"CONTROL_DATABASE_URL": "postgres://control", "SHARD_DATABASE_URLS": "postgres://s0,postgres://s1", "ADMIN_TOKEN": strings.Repeat("a", 32), "COOKIE_SECRET": strings.Repeat("b", 32), "DEDUP_SECRET": strings.Repeat("c", 32), "PUBLIC_ORIGIN": "http://localhost:8080", "COOKIE_SECURE": "false"} {
		t.Setenv(k, v)
	}
}
func TestConfigValidation(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil || len(c.ShardURLs) != 2 {
		t.Fatal(c, err)
	}
	t.Setenv("WORKERS_PER_SHARD", "12")
	if _, err = Load(); err == nil {
		t.Fatal("pool smaller than worker count")
	}
}
func TestSecretsAndSecureCookie(t *testing.T) {
	base(t)
	t.Setenv("COOKIE_SECRET", strings.Repeat("a", 32))
	if _, err := Load(); err == nil {
		t.Fatal("shared secrets")
	}
	t.Setenv("COOKIE_SECRET", strings.Repeat("b", 32))
	t.Setenv("PUBLIC_ORIGIN", "https://poll.example")
	if _, err := Load(); err == nil {
		t.Fatal("https insecure cookie")
	}
}
func TestDuplicateShardsRejected(t *testing.T) {
	base(t)
	t.Setenv("SHARD_DATABASE_URLS", "postgres://s0,postgres://s0")
	if _, err := Load(); err == nil {
		t.Fatal("duplicate shards")
	}
}

func TestHostingPortAndExplicitAddress(t *testing.T) {
	base(t)
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("PORT", "10000")
	c, err := Load()
	if err != nil || c.Addr != ":10000" {
		t.Fatal(c.Addr, err)
	}
	t.Setenv("PORT", "invalid")
	if _, err = Load(); err == nil {
		t.Fatal("invalid hosting port accepted")
	}
	t.Setenv("HTTP_ADDR", ":8081")
	c, err = Load()
	if err != nil || c.Addr != ":8081" {
		t.Fatal(c.Addr, err)
	}
}
