package config

import (
	"os"
	"strings"
	"testing"
)

var testFingerprint = strings.Repeat("ab", 32)

// setRequired は必須の環境変数を設定する。
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("RADIUS_VALKEY_PASSWORD", "secret")
	t.Setenv("RADIUS_ADMIN_TLS_CERT", "/certs/server.pem")
	t.Setenv("RADIUS_ADMIN_TLS_KEY", "/certs/server.key")
	t.Setenv("RADIUS_ADMIN_CLIENTS", "bff-01="+testFingerprint)
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)
	for _, k := range []string{"RADIUS_ADMIN_LISTEN_ADDR", "RADIUS_ADMIN_NODE_NAME", "RADIUS_VALKEY_HOST", "RADIUS_VALKEY_PORT", "RADIUS_LOG_LEVEL", "RADIUS_LOG_MASK_IMSI", "RADIUS_ADMIN_AUDIT_MAX"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != ":9444" || cfg.ValkeyAddr() != "valkey:6379" || cfg.LogLevel != "INFO" || cfg.LogMaskIMSI {
		t.Errorf("defaults = %+v", cfg)
	}
	if cfg.AdminClients[testFingerprint] != "bff-01" {
		t.Errorf("AdminClients = %v", cfg.AdminClients)
	}
	if cfg.AuditMax != 10000 {
		t.Errorf("AuditMax = %d", cfg.AuditMax)
	}
	if host, _ := os.Hostname(); cfg.NodeName != host {
		t.Errorf("NodeName = %q, want hostname %q", cfg.NodeName, host)
	}
}

func TestLoad_Values(t *testing.T) {
	setRequired(t)
	t.Setenv("RADIUS_ADMIN_LISTEN_ADDR", "127.0.0.1:19444")
	t.Setenv("RADIUS_ADMIN_NODE_NAME", "minipc-01")
	t.Setenv("RADIUS_VALKEY_HOST", "localhost")
	t.Setenv("RADIUS_VALKEY_PORT", "16379")
	t.Setenv("RADIUS_LOG_MASK_IMSI", "true")
	t.Setenv("RADIUS_ADMIN_AUDIT_MAX", "500")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:19444" || cfg.NodeName != "minipc-01" || cfg.ValkeyAddr() != "localhost:16379" || !cfg.LogMaskIMSI ||
		cfg.AuditMax != 500 || cfg.ValkeyPassword != "secret" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"empty admin clients", map[string]string{"RADIUS_ADMIN_CLIENTS": " , "}},
		{"bad admin clients", map[string]string{"RADIUS_ADMIN_CLIENTS": "bff-01=xyz"}},
		{"empty cert", map[string]string{"RADIUS_ADMIN_TLS_CERT": ""}},
		{"bad bool", map[string]string{"RADIUS_LOG_MASK_IMSI": "maybe"}},
		{"zero audit max", map[string]string{"RADIUS_ADMIN_AUDIT_MAX": "0"}},
		{"bad audit max", map[string]string{"RADIUS_ADMIN_AUDIT_MAX": "many"}},
		{"empty valkey password", map[string]string{"RADIUS_VALKEY_PASSWORD": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Error("Load() expected error")
			}
		})
	}

	t.Run("missing admin clients", func(t *testing.T) {
		setRequired(t)
		_ = os.Unsetenv("RADIUS_ADMIN_CLIENTS")
		if _, err := Load(); err == nil {
			t.Error("Load() expected error")
		}
	})
}
