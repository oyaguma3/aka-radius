package radiussrv

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"layeh.com/radius"
)

func TestNewServer(t *testing.T) {
	handler := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {})
	secretSource := radius.StaticSecretSource([]byte("test-secret"))

	s := NewServer(":1812", handler, secretSource)
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.ps == nil {
		t.Fatal("PacketServer is nil")
	}
	if s.ps.Addr != ":1812" {
		t.Errorf("Addr: got %q, want %q", s.ps.Addr, ":1812")
	}
}

func TestNewServer_CustomAddr(t *testing.T) {
	handler := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {})
	secretSource := radius.StaticSecretSource([]byte("secret"))

	s := NewServer(":1813", handler, secretSource)
	if s.ps.Addr != ":1813" {
		t.Errorf("Addr: got %q, want %q", s.ps.Addr, ":1813")
	}
}

func TestNewServer_PacketServerSettings(t *testing.T) {
	handler := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {})
	s := NewServer(":1812", handler, radius.StaticSecretSource([]byte("secret")))

	if !s.ps.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be true (packets are verified by the handler)")
	}
	if s.ps.ErrorLog == nil {
		t.Error("ErrorLog must be set")
	}
}

// TestServer_PacketsReachHandler は、ライブラリで検証される種類のパケット（Accounting-Request）や
// 未知の Code のパケットも、ライブラリに捨てられずにハンドラーへ届くことを確かめる
// （ハンドラーが PKT_UNKNOWN_CODE を送信元IP付きで記録できるようにするため）。
func TestServer_PacketsReachHandler(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket failed: %v", err)
	}

	received := make(chan radius.Code, 4)
	handler := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		received <- r.Code
	})
	s := NewServer(pc.LocalAddr().String(), handler, radius.StaticSecretSource([]byte("testing123")))
	go func() { _ = s.Serve(pc) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	for _, code := range []radius.Code{radius.CodeAccountingRequest, radius.Code(99)} {
		// 別のシークレットで Request Authenticator を計算した（ライブラリの検証なら捨てられる）パケット
		p := radius.New(code, []byte("wrong-secret"))
		b, err := p.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary failed: %v", err)
		}
		if _, err := conn.Write(b); err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		select {
		case got := <-received:
			if got != code {
				t.Errorf("handler received code %d, want %d", got, code)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("packet with code %d did not reach the handler", code)
		}
	}
}

func TestRecoverHandler(t *testing.T) {
	// パケットの処理中のパニックは回復し、SYS_PANIC のログを出す（プロセスは落ちない）。
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	h := recoverHandler(radius.HandlerFunc(func(radius.ResponseWriter, *radius.Request) {
		panic("boom")
	}))
	req := &radius.Request{
		Packet:     radius.New(radius.CodeAccessRequest, []byte("secret")),
		RemoteAddr: &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 50000},
	}
	h.ServeRADIUS(nil, req)

	out := buf.String()
	for _, want := range []string{`"event_id":"SYS_PANIC"`, `"src_ip":"192.0.2.10"`, `"panic":"boom"`, `"stack":`} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %s: %s", want, out)
		}
	}
}

func TestRecoverHandler_PassThrough(t *testing.T) {
	called := false
	h := recoverHandler(radius.HandlerFunc(func(radius.ResponseWriter, *radius.Request) { called = true }))
	h.ServeRADIUS(nil, &radius.Request{Packet: radius.New(radius.CodeAccessRequest, []byte("secret"))})
	if !called {
		t.Error("handler was not called")
	}
}

// startGuarded は、Guard を付けたハンドラーで本物の UDP のサーバーを立て、つないだ UDP の接続を返す。
func startGuarded(t *testing.T, h radius.Handler) (*Server, net.Conn) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(pc.LocalAddr().String(), NewGuard("AUTH", GuardConfig{RequestTimeout: 4 * time.Second, MaxInFlight: 16}, h),
		radius.StaticSecretSource([]byte("testing123")))
	go func() { _ = s.Serve(pc) }()
	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return s, conn
}

func readResponse(t *testing.T, conn net.Conn, wait time.Duration) *radius.Packet {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	p, err := radius.Parse(buf[:n], []byte("testing123"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestServer_RetransmissionRightAfterResponse は、応答を送った直後（ハンドラーが戻る前）に届いた再送にも、
// 前の応答を送り直すことを、本物の UDP で確かめる（段階 2-6 の確認事項 M11。ライブラリの重複の判定に捨てさせない）。
func TestServer_RetransmissionRightAfterResponse(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		calls.Add(1)
		_ = w.Write(r.Response(radius.CodeAccessAccept))
		<-release // 応答の後のログや後片付けがまだ終わっていない
	})
	s, conn := startGuarded(t, h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	defer close(release)

	req := radius.New(radius.CodeAccessRequest, []byte("testing123"))
	raw, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"最初の要求", "再送"} {
		if _, err := conn.Write(raw); err != nil {
			t.Fatal(err)
		}
		resp := readResponse(t, conn, 2*time.Second)
		if resp == nil || resp.Code != radius.CodeAccessAccept {
			t.Fatalf("%s（%d 回目）への応答 = %v", name, i+1, resp)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("handler calls = %d, want 1 (the retransmission is replayed from the cache)", calls.Load())
	}
}

// TestServer_ShutdownWaitsForInFlight は、停止のときに処理中のパケットが応答を送り終えるまで待つことを確かめる。
func TestServer_ShutdownWaitsForInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		close(started)
		<-release
		_ = w.Write(r.Response(radius.CodeAccessAccept))
	})
	s, conn := startGuarded(t, h)
	raw, err := radius.New(radius.CodeAccessRequest, []byte("testing123")).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	<-started

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- s.Shutdown(ctx)
	}()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned before the in-flight packet was answered")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if resp := readResponse(t, conn, 2*time.Second); resp == nil || resp.Code != radius.CodeAccessAccept {
		t.Fatalf("response during shutdown = %v", resp)
	}
	if err := <-stopped; err != nil {
		t.Errorf("Shutdown = %v", err)
	}

	// 停止を始めた後のパケットは処理しない
	if _, err := conn.Write(raw); err == nil {
		if resp := readResponse(t, conn, 300*time.Millisecond); resp != nil {
			t.Errorf("response after shutdown = %v", resp)
		}
	}
}
