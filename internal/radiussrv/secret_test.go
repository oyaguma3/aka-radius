package radiussrv

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeClientStore は試験用の ClientStore。calls に問い合わせた IP を記録する。
type fakeClientStore struct {
	secrets map[string]string
	err     error
	calls   []string
}

func (f *fakeClientStore) GetClientSecret(_ context.Context, ip string) (string, error) {
	f.calls = append(f.calls, ip)
	return f.secrets[ip], f.err
}

func TestSecretSource_ValkeyFound(t *testing.T) {
	mockCS := &fakeClientStore{secrets: map[string]string{"192.168.1.100": "found-secret"}}

	ss := NewSecretSource(mockCS, "fallback")

	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 1812}
	secret, err := ss.RADIUSSecret(context.Background(), addr)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if string(secret) != "found-secret" {
		t.Errorf("secret: got %q, want %q", string(secret), "found-secret")
	}
}

func TestSecretSource_Fallback(t *testing.T) {
	mockCS := &fakeClientStore{secrets: map[string]string{"192.168.1.100": ""}} // 未登録

	ss := NewSecretSource(mockCS, "fallback-secret")

	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 1812}
	secret, err := ss.RADIUSSecret(context.Background(), addr)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if string(secret) != "fallback-secret" {
		t.Errorf("secret: got %q, want %q", string(secret), "fallback-secret")
	}
}

func TestSecretSource_NoSecret(t *testing.T) {
	mockCS := &fakeClientStore{secrets: map[string]string{"192.168.1.100": ""}} // 未登録

	ss := NewSecretSource(mockCS, "") // フォールバックなし

	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 1812}
	secret, err := ss.RADIUSSecret(context.Background(), addr)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if secret != nil {
		t.Errorf("secret: got %v, want nil", secret)
	}
}

func TestSecretSource_ValkeyError(t *testing.T) {
	mockCS := &fakeClientStore{err: errors.New("valkey unavailable")}

	ss := NewSecretSource(mockCS, "fallback-secret")

	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 1812}
	secret, err := ss.RADIUSSecret(context.Background(), addr)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if string(secret) != "fallback-secret" {
		t.Errorf("secret: got %q, want %q", string(secret), "fallback-secret")
	}
}

func TestSecretSource_IPExtraction(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want string
	}{
		{
			"UDPAddr IPv4",
			&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1812},
			"10.0.0.1",
		},
		{
			"UDPAddr IPv6",
			&net.UDPAddr{IP: net.ParseIP("::1"), Port: 1812},
			"::1",
		},
		{
			"TCPAddr",
			&net.TCPAddr{IP: net.ParseIP("172.16.0.1"), Port: 1812},
			"172.16.0.1",
		},
		{
			"nil addr",
			nil,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractIP(tt.addr)
			if got != tt.want {
				t.Errorf("ExtractIP: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSecretSource_NilAddr_WithFallback(t *testing.T) {
	mockCS := &fakeClientStore{}

	ss := NewSecretSource(mockCS, "fallback-secret")

	// nilアドレス → IP抽出失敗 → フォールバック
	secret, err := ss.RADIUSSecret(context.Background(), nil)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if string(secret) != "fallback-secret" {
		t.Errorf("secret: got %q, want %q", string(secret), "fallback-secret")
	}
	if len(mockCS.calls) != 0 {
		t.Errorf("GetClientSecret was called: %v", mockCS.calls)
	}
}

func TestSecretSource_NilAddr_NoFallback(t *testing.T) {
	mockCS := &fakeClientStore{}

	ss := NewSecretSource(mockCS, "")

	// nilアドレス + フォールバックなし → nil
	secret, err := ss.RADIUSSecret(context.Background(), nil)
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if secret != nil {
		t.Errorf("secret: got %v, want nil", secret)
	}
}

func TestClientStore(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.HSet("client:192.168.1.1", "secret", "testing123")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cs := NewClientStore(rdb)

	secret, err := cs.GetClientSecret(context.Background(), "192.168.1.1")
	if err != nil || secret != "testing123" {
		t.Errorf("GetClientSecret(registered) = %q, %v; want %q, nil", secret, err, "testing123")
	}
	secret, err = cs.GetClientSecret(context.Background(), "10.0.0.99")
	if err != nil || secret != "" {
		t.Errorf("GetClientSecret(unregistered) = %q, %v; want empty, nil", secret, err)
	}

	mr.Close()
	if _, err := cs.GetClientSecret(context.Background(), "192.168.1.1"); err == nil || errors.Is(err, redis.Nil) {
		t.Errorf("GetClientSecret(valkey down) error = %v, want a connection error", err)
	}
}
