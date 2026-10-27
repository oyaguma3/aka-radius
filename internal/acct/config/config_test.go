package config

import (
	"os"
	"testing"
	"time"

	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// setRequiredEnv は必須環境変数をすべて設定する
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RADIUS_VALKEY_HOST", "localhost")
	t.Setenv("RADIUS_VALKEY_PORT", "6379")
	t.Setenv("RADIUS_VALKEY_PASSWORD", "secret")
}

func TestLoad(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RADIUS_ACCT_LISTEN_ADDR", ":1814")
	t.Setenv("RADIUS_FALLBACK_SECRET", "testing123")
	t.Setenv("RADIUS_LOG_MASK_IMSI", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.ValkeyHost != "localhost" {
		t.Errorf("ValkeyHost = %q, want %q", cfg.ValkeyHost, "localhost")
	}
	if cfg.ValkeyPort != "6379" {
		t.Errorf("ValkeyPort = %q, want %q", cfg.ValkeyPort, "6379")
	}
	if cfg.ValkeyPassword != "secret" {
		t.Errorf("ValkeyPassword = %q, want %q", cfg.ValkeyPassword, "secret")
	}
	if cfg.ListenAddr != ":1814" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":1814")
	}
	if cfg.FallbackSecret != "testing123" {
		t.Errorf("FallbackSecret = %q, want %q", cfg.FallbackSecret, "testing123")
	}
	if cfg.LogMaskIMSI != true {
		t.Errorf("LogMaskIMSI = %v, want %v", cfg.LogMaskIMSI, true)
	}
}

func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.ListenAddr != ":1813" {
		t.Errorf("ListenAddr default = %q, want %q", cfg.ListenAddr, ":1813")
	}
	if cfg.LogMaskIMSI != false {
		t.Errorf("LogMaskIMSI default = %v, want %v", cfg.LogMaskIMSI, false)
	}
	if cfg.LogLevel != "INFO" {
		t.Errorf("LogLevel default = %q, want %q", cfg.LogLevel, "INFO")
	}
	if cfg.FallbackSecret != "" {
		t.Errorf("FallbackSecret default = %q, want %q", cfg.FallbackSecret, "")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	setRequiredEnv(t)
	os.Unsetenv("RADIUS_VALKEY_PASSWORD")
	if _, err := Load(); err == nil {
		t.Error("Load() should return error when RADIUS_VALKEY_PASSWORD is missing")
	}
}

func TestValkeyAddr(t *testing.T) {
	cfg := &Config{Common: envconf.Common{
		ValkeyHost: "redis.example.com",
		ValkeyPort: "6380",
	}}
	got := cfg.ValkeyAddr()
	want := "redis.example.com:6380"
	if got != want {
		t.Errorf("ValkeyAddr() = %q, want %q", got, want)
	}
}

func TestConstants(t *testing.T) {
	if ValkeyConnectTimeout != 3*time.Second {
		t.Errorf("ValkeyConnectTimeout = %v, want %v", ValkeyConnectTimeout, 3*time.Second)
	}
	if ValkeyCommandTimeout != 2*time.Second {
		t.Errorf("ValkeyCommandTimeout = %v, want %v", ValkeyCommandTimeout, 2*time.Second)
	}
	if ValkeyPoolSize != 10 {
		t.Errorf("ValkeyPoolSize = %d, want %d", ValkeyPoolSize, 10)
	}
	if SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want %v", SessionTTL, 24*time.Hour)
	}
	if DuplicateDetectTTL != 24*time.Hour {
		t.Errorf("DuplicateDetectTTL = %v, want %v", DuplicateDetectTTL, 24*time.Hour)
	}
	if ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", ShutdownTimeout, 5*time.Second)
	}
}

func TestLoadLogLevel(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("RADIUS_LOG_LEVEL", "DEBUG")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "DEBUG")
	}
}
