package radiussrv

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"layeh.com/radius"
)

// recordWriter は送られた応答を記録する。
type recordWriter struct {
	mu   sync.Mutex
	pkts []*radius.Packet
}

func (w *recordWriter) Write(p *radius.Packet) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pkts = append(w.pkts, p)
	return nil
}

func (w *recordWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pkts)
}

func testRequest(id byte, port int) *radius.Request {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))
	p.Identifier = id
	return &radius.Request{
		Packet:     p,
		RemoteAddr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: port},
	}
}

func testConfig() GuardConfig {
	return GuardConfig{RequestTimeout: time.Second, MaxInFlight: 4}
}

func TestGuard_ReplaysCachedResponse(t *testing.T) {
	var calls atomic.Int32
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		calls.Add(1)
		_ = w.Write(r.Response(radius.CodeAccessAccept))
	})
	g := NewGuard("AUTH", testConfig(), h)
	w := &recordWriter{}
	req := testRequest(7, 50000)

	g.ServeRADIUS(w, req)
	g.ServeRADIUS(w, req) // 同じ要求の再送

	if calls.Load() != 1 {
		t.Errorf("handler calls = %d, want 1", calls.Load())
	}
	if w.count() != 2 || w.pkts[0] != w.pkts[1] {
		t.Fatalf("responses = %d (same=%v)", w.count(), w.count() == 2 && w.pkts[0] == w.pkts[1])
	}
	// Encode の結果も同じ（応答の Authenticator は要求の Authenticator から計算する）
	a, _ := w.pkts[0].Encode()
	b, _ := w.pkts[1].Encode()
	if string(a) != string(b) {
		t.Error("replayed response bytes differ")
	}

	// 送信元のポート・Identifier・Authenticator のどれかが違えば別の要求
	other := testRequest(7, 50001)
	g.ServeRADIUS(w, other)
	diffAuth := testRequest(7, 50000)
	diffAuth.Authenticator[0] ^= 0xff
	g.ServeRADIUS(w, diffAuth)
	if calls.Load() != 3 {
		t.Errorf("handler calls = %d, want 3", calls.Load())
	}
}

func TestGuard_DropsDuplicateInFlight(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		calls.Add(1)
		close(started)
		<-release
		_ = w.Write(r.Response(radius.CodeAccessChallenge))
	})
	g := NewGuard("AUTH", testConfig(), h)
	w := &recordWriter{}
	req := testRequest(1, 50000)

	done := make(chan struct{})
	go func() { g.ServeRADIUS(w, req); close(done) }()
	<-started
	g.ServeRADIUS(w, req) // 処理中の重複は捨てる
	if w.count() != 0 {
		t.Fatalf("in-flight duplicate got a response")
	}
	close(release)
	<-done
	g.ServeRADIUS(w, req) // 処理が終わった後の再送には応答を送り直す
	if calls.Load() != 1 || w.count() != 2 {
		t.Errorf("calls=%d responses=%d, want 1 2", calls.Load(), w.count())
	}
}

func TestGuard_NoResponseIsNotCached(t *testing.T) {
	var calls atomic.Int32
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) { calls.Add(1) })
	g := NewGuard("ACCT", testConfig(), h)
	req := testRequest(1, 50000)
	g.ServeRADIUS(&recordWriter{}, req)
	g.ServeRADIUS(&recordWriter{}, req)
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (a dropped request is processed again)", calls.Load())
	}
	if g.cache.size() != 0 {
		t.Errorf("cache size = %d", g.cache.size())
	}
}

func TestGuard_PanicDoesNotLeaveInFlight(t *testing.T) {
	var calls atomic.Int32
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
	})
	g := NewGuard("AUTH", testConfig(), recoverHandler(h))
	req := testRequest(1, 50000)
	g.ServeRADIUS(&recordWriter{}, req)
	g.ServeRADIUS(&recordWriter{}, req)
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestGuard_DropsOverLimit(t *testing.T) {
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	var calls atomic.Int32
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		if calls.Add(1) <= 2 {
			started.Done()
		}
		<-release
	})
	g := NewGuard("AUTH", GuardConfig{RequestTimeout: time.Second, MaxInFlight: 2}, h)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { g.ServeRADIUS(&recordWriter{}, testRequest(byte(i), 50000)) })
	}
	started.Wait()
	g.ServeRADIUS(&recordWriter{}, testRequest(9, 50000)) // 上限を超えたので捨てる
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	close(release)
	wg.Wait()
	// 捨てた要求の印は残らず、次の再送は処理する
	g.ServeRADIUS(&recordWriter{}, testRequest(9, 50000))
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestGuard_RequestDeadline(t *testing.T) {
	var deadline time.Time
	var ok bool
	h := radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		deadline, ok = r.Context().Deadline()
	})
	g := NewGuard("AUTH", GuardConfig{RequestTimeout: 4 * time.Second, MaxInFlight: 1}, h)
	start := time.Now()
	g.ServeRADIUS(&recordWriter{}, testRequest(1, 50000))
	if !ok || deadline.Sub(start) < 3*time.Second || deadline.Sub(start) > 5*time.Second {
		t.Errorf("deadline = %v (ok=%v)", deadline.Sub(start), ok)
	}
}

func TestReplayCache_Evict(t *testing.T) {
	c := newReplayCache(30*time.Second, 3)
	now := time.Now()
	pkt := radius.New(radius.CodeAccessAccept, nil)
	for i := range 5 {
		k := replayKey{addr: "a", identifier: byte(i)}
		c.begin(k, now)
		c.finish(k, pkt, now)
	}
	if c.size() != 3 {
		t.Errorf("size = %d, want 3 (max)", c.size())
	}
	// 期限を過ぎたものは消える
	c.begin(replayKey{addr: "b"}, now.Add(31*time.Second))
	if c.size() != 1 {
		t.Errorf("size after ttl = %d, want 1", c.size())
	}
	// abort の後に入れ直したキーは、古い queue の項目で消されない
	k := replayKey{addr: "c"}
	c.begin(k, now.Add(32*time.Second))
	c.abort(k)
	c.begin(k, now.Add(40*time.Second))
	c.evict(now.Add(63 * time.Second)) // 最初の記録（32 秒）の期限は過ぎたが、入れ直し（40 秒）は残る
	if _, st := c.begin(k, now.Add(63*time.Second)); st != replayInFlight {
		t.Errorf("state = %v, want in flight", st)
	}
}

func TestLoadGuardConfig(t *testing.T) {
	cfg, err := LoadGuardConfig()
	if err != nil || cfg.RequestTimeout != DefaultRequestTimeout || cfg.MaxInFlight != DefaultMaxInFlight {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	t.Setenv("RADIUS_REQUEST_TIMEOUT", "2s")
	t.Setenv("RADIUS_MAX_INFLIGHT", "10")
	if cfg, err = LoadGuardConfig(); err != nil || cfg.RequestTimeout != 2*time.Second || cfg.MaxInFlight != 10 {
		t.Fatalf("set: %+v %v", cfg, err)
	}
	t.Setenv("RADIUS_REQUEST_TIMEOUT", "1h")
	if _, err := LoadGuardConfig(); err == nil {
		t.Error("out of range timeout accepted")
	}
	t.Setenv("RADIUS_REQUEST_TIMEOUT", "")
	t.Setenv("RADIUS_MAX_INFLIGHT", "0")
	if _, err := LoadGuardConfig(); err == nil {
		t.Error("zero max in flight accepted")
	}
}
