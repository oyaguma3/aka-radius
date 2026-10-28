package usage

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestDayAndPlace(t *testing.T) {
	// 日本時間の日付で区切る（UTC の 15 時は日本時間の翌日 0 時）
	if d := Day(time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)); d != "20261029" {
		t.Errorf("Day = %s", d)
	}
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, JST)
	tests := []struct {
		name   string
		event  time.Time
		day    string
		tooOld bool
		future bool
	}{
		{"Event-Timestamp なし", time.Time{}, "20261028", false, false},
		{"前の日", now.Add(-5 * time.Hour), "20261027", false, false},
		{"7 日前の始まり", time.Date(2026, 10, 21, 0, 0, 0, 0, JST), "20261021", false, false},
		{"7 日より前", time.Date(2026, 10, 20, 23, 59, 0, 0, JST), "", true, false},
		{"少し先", now.Add(30 * time.Minute), "20261028", false, false},
		{"1 時間以上先は受け取った時刻", now.Add(2 * 24 * time.Hour), "20261028", false, true},
	}
	for _, tt := range tests {
		p := Place(tt.event, now)
		if p.Day != tt.day || p.TooOld != tt.tooOld || p.Future != tt.future {
			t.Errorf("%s: %+v", tt.name, p)
		}
	}
	exp, err := ExpireAt("20261028")
	if err != nil || !exp.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, JST)) {
		t.Errorf("ExpireAt = %v %v", exp, err)
	}
	if _, err := ExpireAt("bad"); err == nil {
		t.Error("bad day accepted")
	}
}

func TestAddAuthOKAndList(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStore(rdb)
	ctx := t.Context()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, JST)
	for _, x := range []struct {
		tenant int64
		imsi   string
	}{{42, "440100000000002"}, {42, "440100000000001"}, {42, "440100000000001"}, {7, "440100000000001"}} {
		if err := s.AddAuthOK(ctx, now, x.tenant, x.imsi); err != nil {
			t.Fatal(err)
		}
	}
	mr.HSet(Key("20261028", 42, "440100000000001"), FieldSessions, "2", FieldSeconds, "60", FieldInOctets, "5000000000", FieldOutOctets, "7")
	if ttl := mr.TTL(Key("20261028", 42, "440100000000001")); ttl <= 0 {
		t.Errorf("ttl = %v", ttl)
	}

	got, next, err := s.List(ctx, "20261028", "", 2)
	if err != nil || len(got) != 2 || next != "42:440100000000002" {
		t.Fatalf("page 1 = %+v %q %v", got, next, err)
	}
	// 辞書順: 42:...001、42:...002、7:...001
	if r := got[0]; r.TenantID != 42 || r.IMSI != "440100000000001" || r.AuthOK != 2 || r.Sessions != 2 || r.Seconds != 60 || r.InOctets != 5000000000 || r.OutOctets != 7 {
		t.Errorf("record = %+v", r)
	}
	got, next, err = s.List(ctx, "20261028", next, 2)
	if err != nil || len(got) != 1 || next != "" || got[0].TenantID != 7 {
		t.Fatalf("page 2 = %+v %q %v", got, next, err)
	}
	if got, _, _ := s.List(ctx, "20261027", "", 10); len(got) != 0 {
		t.Errorf("other day = %+v", got)
	}
}
