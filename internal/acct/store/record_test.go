package store

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/usage"
	"github.com/redis/go-redis/v9"
)

const (
	testIMSI = "440100123456789"
	testDay  = "20261028"
)

type recEnv struct {
	mr   *miniredis.Miniredis
	rs   *RecordStore
	sess *sessions.Store
	now  time.Time
}

func newRecEnv(t *testing.T) *recEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &recEnv{mr: mr, rs: NewRecordStore(rdb), sess: sessions.NewStore(rdb), now: time.Date(2026, 10, 28, 12, 0, 0, 0, usage.JST)}
}

func (e *recEnv) rec(kind Kind, sec, in, out uint64) Record {
	return Record{
		Kind: kind, SeenKey: SeenKey("192.0.2.1", "ap-1", "acct-1"), Seconds: sec, InOctets: in, OutOctets: out,
		StampTenant: 42, Day: testDay, Now: e.now, NasIP: "192.0.2.1", NasIdentifier: "ap-1", AcctSessionID: "acct-1",
	}
}

func (e *recEnv) account(t *testing.T, r Record) Result {
	t.Helper()
	res, err := e.rs.Account(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *recEnv) usage(field string, tenant int64) string {
	return e.mr.HGet(usage.Key(testDay, tenant, testIMSI), field)
}

func TestAccount_IncrementsAndDuplicates(t *testing.T) {
	e := newRecEnv(t)
	if err := e.sess.Create(t.Context(), sessions.Session{ID: "s1", IMSI: testIMSI, TenantID: 42, NasIP: "192.0.2.1"}, e.now); err != nil {
		t.Fatal(err)
	}
	withSess := func(r Record) Record { r.SessionID = "s1"; return r }

	res := e.account(t, withSess(e.rec(KindStart, 0, 0, 0)))
	if res.Duplicate || !res.SessionFound || res.IMSI != testIMSI || res.TenantID != 42 || res.TenantSource != "stamp" || !res.UsageWritten {
		t.Fatalf("start = %+v", res)
	}
	if e.account(t, withSess(e.rec(KindStart, 0, 0, 0))); e.usage(usage.FieldSessions, 42) != "1" {
		t.Errorf("sessions = %s (a duplicate start must not count)", e.usage(usage.FieldSessions, 42))
	}

	res = e.account(t, withSess(e.rec(KindInterim, 60, 1000, 2000)))
	if res.Duplicate || res.Seconds != 60 || res.InOctets != 1000 || res.OutOctets != 2000 {
		t.Fatalf("interim 1 = %+v", res)
	}
	// 同じ値の再送は重複で、増分は 0
	if res = e.account(t, withSess(e.rec(KindInterim, 60, 1000, 2000))); !res.Duplicate || res.Seconds != 0 {
		t.Fatalf("interim dup = %+v", res)
	}
	// Gigawords を含む大きな累計（2^32 を超える）
	big := uint64(5) << 32
	res = e.account(t, withSess(e.rec(KindInterim, 120, big+1000, 2500)))
	if res.Seconds != 60 || res.InOctets != int64(big) || res.OutOctets != 500 {
		t.Fatalf("interim 2 = %+v", res)
	}
	if got := e.mr.HGet(sessions.Key("s1"), "input_octets"); got != "21474837480" {
		t.Errorf("session input_octets = %s", got)
	}
	res = e.account(t, withSess(e.rec(KindStop, 180, big+1500, 3000)))
	if res.Seconds != 60 || res.InOctets != 500 || res.OutOctets != 500 {
		t.Fatalf("stop = %+v", res)
	}
	// Stop の後の Stop は重複
	if res = e.account(t, withSess(e.rec(KindStop, 180, big+1500, 3000))); !res.Duplicate {
		t.Fatalf("stop dup = %+v", res)
	}

	if e.usage(usage.FieldSeconds, 42) != "180" || e.usage(usage.FieldInOctets, 42) != "21474837980" || e.usage(usage.FieldOutOctets, 42) != "3000" {
		t.Errorf("usage = %s %s %s", e.usage(usage.FieldSeconds, 42), e.usage(usage.FieldInOctets, 42), e.usage(usage.FieldOutOctets, 42))
	}
	if ttl := e.mr.TTL(usage.Key(testDay, 42, testIMSI)); ttl <= 0 {
		t.Errorf("usage ttl = %v", ttl)
	}
	if m, _ := e.mr.ZMembers(usage.IndexKey(testDay)); len(m) != 1 || m[0] != "42:"+testIMSI {
		t.Errorf("usage index = %v", m)
	}
	// Stop でセッションと索引が消える
	if e.mr.Exists(sessions.Key("s1")) || e.mr.Exists(sessions.IMSIIndexKey(testIMSI)) || e.mr.Exists(sessions.TenantIndexKey(42)) {
		t.Error("session or index remains after stop")
	}
}

func TestAccount_SequenceAndReset(t *testing.T) {
	e := newRecEnv(t)
	r := e.rec(KindInterim, 100, 100, 100)
	r.UserNameIMSI = testIMSI
	// Start のない Interim は、累計をそのまま増分にする
	res := e.account(t, r)
	if res.Sequence != "no_start_received" || res.Seconds != 100 || res.SessionFound || res.IMSI != testIMSI {
		t.Fatalf("interim without start = %+v", res)
	}
	// 累計が減ったら、その値をそのまま増分にする
	r = e.rec(KindInterim, 10, 5, 5)
	if res = e.account(t, r); !res.CounterReset || res.Seconds != 10 || res.InOctets != 5 || res.IMSI != testIMSI {
		t.Fatalf("reset = %+v", res)
	}
	if res = e.account(t, e.rec(KindStop, 20, 5, 5)); res.Seconds != 10 || res.InOctets != 0 {
		t.Fatalf("stop = %+v", res)
	}
	if res = e.account(t, e.rec(KindInterim, 30, 5, 5)); res.Sequence != "interim_after_stop" {
		t.Fatalf("interim after stop = %+v", res)
	}
	r = e.rec(KindStop, 0, 0, 0)
	r.SeenKey = SeenKey("192.0.2.1", "ap-1", "acct-2")
	r.UserNameIMSI = testIMSI
	if res = e.account(t, r); res.Sequence != "no_start_received" {
		t.Fatalf("stop without start = %+v", res)
	}
	// Stop の後の Start は新しいセッションとして 0 から数える
	r.Kind = KindStart
	if res = e.account(t, r); res.Sequence != "start_after_stop" || res.Duplicate {
		t.Fatalf("start after stop = %+v", res)
	}
	r.Kind, r.Seconds = KindInterim, 7
	if res = e.account(t, r); res.Seconds != 7 {
		t.Fatalf("interim after restart = %+v", res)
	}
}

func TestAccount_TenantAndIMSISources(t *testing.T) {
	e := newRecEnv(t)
	if err := e.sess.Create(t.Context(), sessions.Session{ID: "s1", IMSI: testIMSI, TenantID: 7}, e.now); err != nil {
		t.Fatal(err)
	}
	// 刻印がなければセッションのテナント
	r := e.rec(KindStart, 0, 0, 0)
	r.StampTenant, r.SessionID = 0, "s1"
	if res := e.account(t, r); res.TenantID != 7 || res.TenantSource != "session" || res.IMSI != testIMSI {
		t.Fatalf("session tenant = %+v", res)
	}
	// セッションがなくても、重複の判定のキーに残したテナントと IMSI を使う
	r = e.rec(KindInterim, 10, 0, 0)
	r.StampTenant = 0
	if res := e.account(t, r); res.TenantID != 7 || res.TenantSource != "seen" || res.IMSI != testIMSI || res.SessionFound {
		t.Fatalf("seen tenant = %+v", res)
	}
	// どれもなければテナント 0。IMSI が分からなければ集計しない
	r = e.rec(KindStart, 0, 0, 0)
	r.SeenKey, r.StampTenant = SeenKey("192.0.2.1", "ap-2", "x"), 0
	if res := e.account(t, r); res.TenantSource != "none" || res.IMSI != "" || res.UsageWritten {
		t.Fatalf("unknown = %+v", res)
	}
	r.SeenKey, r.UserNameIMSI = SeenKey("192.0.2.1", "ap-3", "x"), testIMSI
	if res := e.account(t, r); res.TenantID != 0 || !res.UsageWritten {
		t.Fatalf("tenant 0 = %+v", res)
	}
	// 日が空（期限を過ぎた日）なら足し込まない
	r.SeenKey, r.Day = SeenKey("192.0.2.1", "ap-4", "x"), ""
	if res := e.account(t, r); res.UsageWritten {
		t.Fatalf("too old = %+v", res)
	}
	// Class のセッションが消えていれば、セッションなしとして扱う
	r = e.rec(KindStart, 0, 0, 0)
	r.SeenKey, r.SessionID = SeenKey("192.0.2.1", "ap-5", "x"), "gone"
	if res := e.account(t, r); res.SessionFound {
		t.Fatalf("gone session = %+v", res)
	}
}

func TestAccount_SessionUpdate(t *testing.T) {
	e := newRecEnv(t)
	if err := e.sess.Create(t.Context(), sessions.Session{ID: "s1", IMSI: testIMSI, TenantID: 42}, e.now); err != nil {
		t.Fatal(err)
	}
	r := e.rec(KindStart, 0, 0, 0)
	r.SessionID, r.CallingStationID, r.FramedIP = "s1", "00-11-22-33-44-55", "10.0.0.5"
	r.Now = e.now.Add(time.Hour)
	e.account(t, r)
	got, _, err := e.sess.ListByTenant(t.Context(), 42, 0, 10, e.now)
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions = %+v %v", got, err)
	}
	s := got[0]
	if s.CallingStationID != "00-11-22-33-44-55" || s.FramedIP != "10.0.0.5" || s.AcctSessionID != "acct-1" || s.StartTime.Unix() != r.Now.Unix() ||
		s.ExpiresAt.Unix() != r.Now.Add(sessions.TTL).Unix() {
		t.Errorf("session = %+v", s)
	}
}

func TestSeenKey(t *testing.T) {
	if k := SeenKey("192.0.2.1", "tenant-ap", "abc"); k != "acct:seen:192.0.2.1:tenant-ap:abc" {
		t.Error(k)
	}
}
