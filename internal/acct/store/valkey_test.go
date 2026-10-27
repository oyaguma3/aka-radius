package store

import (
	"net"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/acct/config"
	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// newTestConfig は miniredis のアドレスから Config を作る。
func newTestConfig(addr string) *config.Config {
	host, port, _ := net.SplitHostPort(addr)
	return &config.Config{Common: envconf.Common{ValkeyHost: host, ValkeyPort: port}}
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
