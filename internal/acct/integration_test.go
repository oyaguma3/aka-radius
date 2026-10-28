package acct

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/acct/config"
	radiuspkg "github.com/oyaguma3/aka-radius/internal/acct/radius"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/usage"
	"github.com/redis/go-redis/v9"
	"layeh.com/radius"
)

// 結合試験: RADIUS の Accounting-Request から、重複の判定・増分・利用の集計・セッションの更新までを通す
// （MVP の設計概要 §10。本物の Valkey での確かめは手元の compose で行う）。

const (
	itSecret = "testing123"
	itIMSI   = "440100123456789"
)

type itEnv struct {
	mr   *miniredis.Miniredis
	rdb  *redis.Client
	addr string
	conn net.PacketConn
}

func startAcct(t *testing.T) *itEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.HSet("client:127.0.0.1", "secret", itSecret, "trust_stamp", "1")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ListenAddr: pc.LocalAddr().String(), StampVendorID: 32473, StampVendorType: 1,
		Guard: radiussrv.GuardConfig{RequestTimeout: 4 * time.Second, MaxInFlight: 16},
	}
	srv := newServer(cfg, rdb)
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &itEnv{mr: mr, rdb: rdb, addr: cfg.ListenAddr, conn: conn}
}

var itID atomic.Uint32

type acctReq struct {
	status          uint32
	sessionTime     uint32
	in, out, inGiga uint32
	class           string
	stamp           string
	event           time.Time
}

// send は Accounting-Request を作って送り、応答（なければ nil）を返す。raw に符号化した要求を返す（再送の試験用）。
func (e *itEnv) send(t *testing.T, r acctReq) (*radius.Packet, []byte) {
	t.Helper()
	p := radius.New(radius.CodeAccountingRequest, []byte(itSecret))
	p.Identifier = byte(itID.Add(1))
	u32 := func(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
	p.Add(radius.Type(radiuspkg.AttrTypeAcctStatusType), u32(r.status))
	p.Add(radius.Type(radiuspkg.AttrTypeAcctSessionID), []byte("acct-1"))
	p.Add(radius.Type(radiuspkg.AttrTypeNASIdentifier), []byte("tenant42-ap-01"))
	p.Add(radius.Type(radiuspkg.AttrTypeUserName), []byte("anonymous@realm"))
	p.Add(radius.Type(radiuspkg.AttrTypeAcctSessionTime), u32(r.sessionTime))
	p.Add(radius.Type(radiuspkg.AttrTypeAcctInputOct), u32(r.in))
	p.Add(radius.Type(radiuspkg.AttrTypeAcctInputGiga), u32(r.inGiga))
	p.Add(radius.Type(radiuspkg.AttrTypeAcctOutputOct), u32(r.out))
	if r.class != "" {
		p.Add(radius.Type(radiuspkg.AttrTypeClass), []byte(r.class))
	}
	if !r.event.IsZero() {
		p.Add(radius.Type(radiuspkg.AttrTypeEventTimestamp), u32(uint32(r.event.Unix())))
	}
	if r.stamp != "" {
		p.Add(26, append([]byte{0, 0, 0x7e, 0xd9, 1, byte(2 + len(r.stamp))}, r.stamp...))
	}
	data, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	h := md5.New()
	h.Write(data[:4])
	h.Write(make([]byte, 16))
	h.Write(data[20:])
	h.Write([]byte(itSecret))
	copy(data[4:20], h.Sum(nil))
	return e.sendRaw(t, data), data
}

func (e *itEnv) sendRaw(t *testing.T, raw []byte) *radius.Packet {
	t.Helper()
	ua, _ := net.ResolveUDPAddr("udp", e.addr)
	if _, err := e.conn.WriteTo(raw, ua); err != nil {
		t.Fatal(err)
	}
	_ = e.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := e.conn.ReadFrom(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil
		}
		t.Fatal(err)
	}
	resp, err := radius.Parse(buf[:n], []byte(itSecret))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *itEnv) usage(t *testing.T, day string, tenant int64) map[string]string {
	t.Helper()
	m, err := e.rdb.HGetAll(context.Background(), usage.Key(day, tenant, itIMSI)).Result()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIntegration_AccountingUsage(t *testing.T) {
	env := startAcct(t)
	now := time.Now()
	if err := sessions.NewStore(env.rdb).Create(context.Background(), sessions.Session{ID: "11111111-1111-1111-1111-111111111111", IMSI: itIMSI, TenantID: 42}, now); err != nil {
		t.Fatal(err)
	}
	class := "11111111-1111-1111-1111-111111111111"
	today := usage.Day(now)

	if resp, _ := env.send(t, acctReq{status: radiuspkg.AcctStatusTypeStart, class: class, stamp: "42"}); resp == nil || resp.Code != radius.CodeAccountingResponse {
		t.Fatalf("start response = %v", resp)
	}
	_, raw := env.send(t, acctReq{status: radiuspkg.AcctStatusTypeInterim, class: class, stamp: "42", sessionTime: 60, in: 1000, inGiga: 1, out: 500})
	// 同じパケットの再送（応答のキャッシュ）と、同じ値の別のパケット（累計からの増分で 0）では数え直さない
	if resp := env.sendRaw(t, raw); resp == nil {
		t.Fatal("no response to a retransmission")
	}
	env.send(t, acctReq{status: radiuspkg.AcctStatusTypeInterim, class: class, stamp: "42", sessionTime: 60, in: 1000, inGiga: 1, out: 500})
	env.send(t, acctReq{status: radiuspkg.AcctStatusTypeStop, class: class, stamp: "42", sessionTime: 90, in: 1500, inGiga: 1, out: 800})

	u := env.usage(t, today, 42)
	if u["sessions"] != "1" || u["seconds"] != "90" || u["in_octets"] != "4294968796" || u["out_octets"] != "800" {
		t.Errorf("usage = %v", u)
	}
	if env.mr.Exists(sessions.Key(class)) {
		t.Error("session remains after stop")
	}
}

func TestIntegration_AccountingWithoutSession(t *testing.T) {
	// Class もセッションもない（User-Name も匿名）と IMSI が分からないので集計しない。応答は返す
	env := startAcct(t)
	if resp, _ := env.send(t, acctReq{status: radiuspkg.AcctStatusTypeStart, stamp: "42"}); resp == nil {
		t.Fatal("no response")
	}
	if keys := env.mr.Keys(); len(keys) != 2 { // client:127.0.0.1 と acct:seen:…
		t.Errorf("keys = %v", keys)
	}

	// Event-Timestamp の日に足し込む（前の日）
	env2 := startAcct(t)
	now := time.Now()
	class := "22222222-2222-2222-2222-222222222222"
	if err := sessions.NewStore(env2.rdb).Create(context.Background(), sessions.Session{ID: class, IMSI: itIMSI, TenantID: 7}, now); err != nil {
		t.Fatal(err)
	}
	yesterday := now.Add(-24 * time.Hour)
	env2.send(t, acctReq{status: radiuspkg.AcctStatusTypeInterim, class: class, sessionTime: 30, event: yesterday})
	// 刻印がなければセッションのテナント
	if u := env2.usage(t, usage.Day(yesterday), 7); u["seconds"] != "30" {
		t.Errorf("usage = %v", u)
	}
}

func TestIntegration_NoResponseWhenValkeyIsDown(t *testing.T) {
	env := startAcct(t)
	env.mr.HSet("client:127.0.0.1", "secret", itSecret)
	// Valkey に届かなければ応答しない（記録できないので、NAS に送り直させる）
	env.mr.Close()
	if resp, _ := env.send(t, acctReq{status: radiuspkg.AcctStatusTypeStart}); resp != nil {
		t.Errorf("response = %v, want none", resp.Code)
	}
}

type fakeSweeper struct {
	calls atomic.Int32
	err   error
}

func (f *fakeSweeper) Sweep(context.Context, time.Time) (int64, error) {
	f.calls.Add(1)
	return 3, f.err
}

func TestSweepSessions(t *testing.T) {
	f := &fakeSweeper{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { sweepSessions(f, 10*time.Millisecond, stop); close(done) }()
	time.Sleep(55 * time.Millisecond)
	close(stop)
	<-done
	if n := f.calls.Load(); n < 3 {
		t.Errorf("calls = %d", n)
	}
	f.err = errors.New("down")
	sweepOnce(f) // 誤りはログだけ
}
