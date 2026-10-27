package store

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// テスト用にminiredisからConfigを生成するヘルパー
func newTestConfig(addr string) *config.Config {
	return &config.Config{
		Common:      envconf.Common{ValkeyHost: splitHost(addr), ValkeyPort: splitPort(addr), ValkeyPassword: ""},
		NetworkName: "WLAN",
	}
}

func splitHost(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func splitPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}

func TestNewValkeyClient(t *testing.T) {
	mr := miniredis.RunT(t)

	cfg := newTestConfig(mr.Addr())
	vc, err := NewValkeyClient(cfg)
	if err != nil {
		t.Fatalf("NewValkeyClient failed: %v", err)
	}
	defer vc.Close()

	if vc.Client() == nil {
		t.Fatal("Client() returned nil")
	}
}

func TestNewValkeyClientConnectionError(t *testing.T) {
	cfg := newTestConfig("127.0.0.1:59999")
	_, err := NewValkeyClient(cfg)
	if err == nil {
		t.Fatal("expected error for invalid address, got nil")
	}
}
