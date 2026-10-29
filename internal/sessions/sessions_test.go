package sessions

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCreateListSweep(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStore(rdb)
	ctx := t.Context()
	now := time.Unix(1_800_000_000, 0)

	for i, x := range []struct {
		id, imsi string
		tenant   int64
	}{{"a", "440100000000001", 42}, {"b", "440100000000002", 42}, {"c", "440100000000001", 7}} {
		if err := s.Create(ctx, Session{ID: x.id, IMSI: x.imsi, TenantID: x.tenant, NasIP: "192.0.2.1", NasIdentifier: "ap-1"}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if ttl := mr.TTL(Key("a")); ttl <= 0 {
		t.Errorf("ttl = %v", ttl)
	}

	got, next, err := s.ListByTenant(ctx, 42, 0, 1, now)
	if err != nil || len(got) != 1 || next != 1 || got[0].ID != "a" || got[0].TenantID != 42 || got[0].NasIdentifier != "ap-1" || got[0].StartTime.Unix() != now.Unix() {
		t.Fatalf("tenant page 1 = %+v %d %v", got, next, err)
	}
	if got, next, _ = s.ListByTenant(ctx, 42, 1, 1, now); len(got) != 1 || next != -1 || got[0].ID != "b" {
		t.Fatalf("tenant page 2 = %+v %d", got, next)
	}
	got, next, err = s.ListByIMSIs(ctx, []string{"440100000000001", "440100000000009"}, 0, 10, now)
	if err != nil || len(got) != 2 || next != -1 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("imsis = %+v %d %v", got, next, err)
	}
	if got, next, _ = s.ListByIMSIs(ctx, []string{"440100000000001"}, 1, 1, now); len(got) != 1 || got[0].ID != "c" || next != -1 {
		t.Fatalf("imsis offset = %+v %d", got, next)
	}
	if got, _, _ = s.ListByIMSIs(ctx, []string{"440100000000001"}, 5, 1, now); len(got) != 0 {
		t.Fatalf("imsis over offset = %+v", got)
	}

	// 索引にあってセッションが消えているものは飛ばす
	mr.Del(Key("b"))
	if got, _, _ = s.ListByTenant(ctx, 42, 0, 10, now); len(got) != 1 {
		t.Errorf("after delete = %+v", got)
	}

	if _, _, ok, _ := s.Stats(ctx); ok {
		t.Error("stats before sweep")
	}
	n, err := s.Sweep(ctx, now)
	if err != nil || n != 3 {
		t.Fatalf("sweep = %d %v", n, err)
	}
	// 期限を過ぎたら索引から消える
	n, err = s.Sweep(ctx, now.Add(TTL+2*time.Second))
	if err != nil || n != 1 {
		t.Fatalf("sweep after expiry = %d %v", n, err)
	}
	count, at, ok, err := s.Stats(ctx)
	if err != nil || !ok || count != 1 || at.Unix() != now.Add(TTL+2*time.Second).Unix() {
		t.Errorf("stats = %d %v %v %v", count, at, ok, err)
	}
	if got, _, _ = s.ListByTenant(ctx, 42, 0, 10, now.Add(TTL+2*time.Second)); len(got) != 0 {
		t.Errorf("expired = %+v", got)
	}
}
