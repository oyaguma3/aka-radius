package clients

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStore(rdb)
	s.now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }
	return s, mr
}

func TestLifecycle(t *testing.T) {
	s, mr := newStore(t)
	ctx := t.Context()
	secret := strings.Repeat("s", 16)
	for _, c := range []Client{
		{IP: "10.0.0.9", Name: "edge-02", Secret: secret, TrustStamp: true},
		{IP: "10.0.0.10", Name: "edge-03", Secret: secret},
		{IP: "2001:db8::1", Name: "edge-v6", Secret: secret, TrustStamp: true},
		{IP: "10.0.0.2", Name: "edge-01", Secret: secret, TrustStamp: true},
	} {
		if err := s.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Add(ctx, Client{IP: "10.0.0.2", Name: "dup", Secret: secret}); !errors.Is(err, ErrExists) {
		t.Errorf("Add(dup) = %v", err)
	}
	// auth・acct が読むのと同じ形で書かれている
	if v := mr.HGet("client:10.0.0.2", "trust_stamp"); v != "1" {
		t.Errorf("trust_stamp = %q", v)
	}
	if v := mr.HGet("client:10.0.0.10", "trust_stamp"); v != "0" {
		t.Errorf("trust_stamp = %q", v)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ips []string
	for _, c := range list {
		ips = append(ips, c.IP)
	}
	if strings.Join(ips, ",") != "10.0.0.2,10.0.0.9,10.0.0.10,2001:db8::1" {
		t.Errorf("List() order = %v", ips)
	}
	if n, _ := s.Count(ctx); n != 4 {
		t.Errorf("Count() = %d", n)
	}

	s.now = func() time.Time { return time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) }
	name, newSecret, off := "edge-01b", strings.Repeat("t", 20), false
	if err := s.Update(ctx, "10.0.0.2", Update{Name: &name, Secret: &newSecret, TrustStamp: &off}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(ctx, "10.0.0.2")
	if err != nil || c.Name != name || c.Secret != newSecret || c.TrustStamp || !c.UpdatedAt.After(c.CreatedAt) {
		t.Errorf("Get() after Update = %+v %v", c, err)
	}
	if err := s.Update(ctx, "10.9.9.9", Update{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) = %v", err)
	}
	if mr.Exists("client:10.9.9.9") {
		t.Error("Update(missing) created the key")
	}

	if err := s.Remove(ctx, "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "10.0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove(again) = %v", err)
	}
	if _, err := s.Get(ctx, "10.0.0.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(removed) = %v", err)
	}
	if n, _ := s.Count(ctx); n != 3 {
		t.Errorf("Count() after Remove = %d", n)
	}
}

func TestListEmpty(t *testing.T) {
	s, _ := newStore(t)
	if list, err := s.List(t.Context()); err != nil || len(list) != 0 {
		t.Errorf("List() = %v %v", list, err)
	}
}

func TestValidation(t *testing.T) {
	for in, want := range map[string]string{"10.0.0.1": "10.0.0.1", "::ffff:10.0.0.1": "10.0.0.1", "2001:DB8:0::1": "2001:db8::1"} {
		if got, err := NormalizeIP(in); err != nil || got != want {
			t.Errorf("NormalizeIP(%q) = %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "edge-01", "10.0.0.1/32", "fe80::1%eth0", "10.0.0.256"} {
		if _, err := NormalizeIP(bad); err == nil {
			t.Errorf("NormalizeIP(%q) accepted", bad)
		}
	}
	if CheckName("edge-01.tokyo_a") != nil || CheckName("") == nil || CheckName("edge 01") == nil || CheckName(strings.Repeat("a", 65)) == nil {
		t.Error("CheckName")
	}
	if CheckSecret(strings.Repeat("a", 16)) != nil || CheckSecret(strings.Repeat("a", 15)) == nil ||
		CheckSecret(strings.Repeat("a", 129)) == nil || CheckSecret(strings.Repeat("a", 16)+" x") == nil {
		t.Error("CheckSecret")
	}
	g := GenerateSecret()
	if CheckSecret(g) != nil || len(g) != 32 || g == GenerateSecret() {
		t.Errorf("GenerateSecret() = %q", g)
	}
}
