package config

import (
	"os"
	"testing"
)

// setRequired は必須の環境変数を設定する。
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("RADIUS_VALKEY_PASSWORD", "secret")
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)
	for _, k := range []string{"RADIUS_ADMIN_LISTEN_ADDR", "RADIUS_ADMIN_NODE_NAME", "RADIUS_VALKEY_HOST", "RADIUS_VALKEY_PORT", "RADIUS_LOG_LEVEL",
		"RADIUS_LOG_MASK_IMSI", "RADIUS_ADMIN_TLS_CERT", "RADIUS_ADMIN_TLS_KEY", "RADIUS_ADMIN_CLIENTS_FILE",
		"RADIUS_AUDIT_DIR", "RADIUS_AUDIT_RETENTION_DAYS", "RADIUS_AUDIT_MAX_LEN", "RADIUS_HEALTH_ADDR"} {
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
	if cfg.TLSCert != "/certs/server.pem" || cfg.TLSKey != "/certs/server.key" || cfg.AdminClientsFile != "/run/secrets/admin-clients" {
		t.Errorf("file defaults = %+v", cfg)
	}
	if cfg.Audit.Dir != "/var/lib/aka-radius/audit" || cfg.Audit.RetentionDays != 14 || cfg.Audit.MaxLen != 10000 {
		t.Errorf("Audit = %+v", cfg.Audit)
	}
	if cfg.HealthAddr != "127.0.0.1:9090" {
		t.Errorf("HealthAddr = %q", cfg.HealthAddr)
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
	t.Setenv("RADIUS_ADMIN_CLIENTS_FILE", "/tmp/pins")
	t.Setenv("RADIUS_AUDIT_DIR", "/tmp/audit")
	t.Setenv("RADIUS_AUDIT_RETENTION_DAYS", "30")
	t.Setenv("RADIUS_AUDIT_MAX_LEN", "500")
	t.Setenv("RADIUS_HEALTH_ADDR", "127.0.0.1:19090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:19444" || cfg.NodeName != "minipc-01" || cfg.ValkeyAddr() != "localhost:16379" || !cfg.LogMaskIMSI ||
		cfg.ValkeyPassword != "secret" || cfg.AdminClientsFile != "/tmp/pins" || cfg.HealthAddr != "127.0.0.1:19090" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Audit.Dir != "/tmp/audit" || cfg.Audit.RetentionDays != 30 || cfg.Audit.MaxLen != 500 {
		t.Errorf("Audit = %+v", cfg.Audit)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"bad bool", map[string]string{"RADIUS_LOG_MASK_IMSI": "maybe"}},
		{"zero audit max", map[string]string{"RADIUS_AUDIT_MAX_LEN": "0"}},
		{"bad audit max", map[string]string{"RADIUS_AUDIT_MAX_LEN": "many"}},
		{"too long retention", map[string]string{"RADIUS_AUDIT_RETENTION_DAYS": "3651"}},
		{"bad health addr", map[string]string{"RADIUS_HEALTH_ADDR": "9090"}},
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
}
