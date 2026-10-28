package subscriber

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewStore(rdb)
	s.now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }
	return s, mr
}

func testSub(imsi, source string) Subscriber {
	return Subscriber{
		IMSI:     imsi,
		State:    StateActive,
		SourceID: source,
		Rules:    []Rule{{TenantID: 42, NasID: "*", SSIDs: []string{"*"}, VlanID: 100}},
	}
}

func count(t *testing.T, mr *miniredis.Miniredis, source string) string {
	t.Helper()
	v, _ := mr.Get(SourceCountKey(source))
	return v
}

func TestCreateGetDelete(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := t.Context()

	if err := s.Create(ctx, testSub("440100000000001", "vecgen")); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testSub("440100000000001", "vecgen")); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v, want ErrExists", err)
	}
	got, err := s.Get(ctx, "440100000000001")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateActive || got.SourceID != "vecgen" || len(got.Rules) != 1 || got.Rules[0].VlanID != 100 ||
		got.CreatedAt.IsZero() || !got.StateChangedAt.Equal(got.CreatedAt) {
		t.Errorf("got %+v", got)
	}
	if c := count(t, mr, "vecgen"); c != "1" {
		t.Errorf("cnt = %q, want 1", c)
	}
	// rules の JSON は既定の値（0）の項目を持たない
	if raw := mr.HGet(Key("440100000000001"), "rules"); raw != `[{"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":100}]` {
		t.Errorf("rules = %s", raw)
	}

	if err := s.Delete(ctx, "440100000000001"); !errors.Is(err, ErrActive) {
		t.Fatalf("delete active: %v, want ErrActive", err)
	}
	if _, _, err := s.SetState(ctx, "440100000000001", StateInactive); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "440100000000001"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "440100000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again: %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "440100000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if c := count(t, mr, "vecgen"); c != "0" {
		t.Errorf("cnt after delete = %q, want 0", c)
	}
	if n, _ := s.Count(ctx); n != 0 {
		t.Errorf("count = %d", n)
	}
}

func TestGetInvalid(t *testing.T) {
	s, mr := newTestStore(t)
	// 本PoC の形の sub:（鍵と SQN だけ）
	mr.HSet(Key("440100000000009"), "ki", "00", "opc", "00", "sqn", "000000000000")
	if _, err := s.Get(t.Context(), "440100000000009"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	mr.HSet(Key("440100000000008"), "state", "active", "source_id", "vecgen", "rules", "{bad")
	if _, err := s.Get(t.Context(), "440100000000008"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad rules: err = %v, want ErrInvalid", err)
	}
}

func TestUpdate(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, testSub("440100000000001", "vecgen")); err != nil {
		t.Fatal(err)
	}
	other := "other"
	rules := []Rule{{TenantID: 7, NasID: "ap-1", SSIDs: []string{"corp"}}}
	if err := s.Update(ctx, "440100000000001", Patch{SourceID: &other, Rules: &rules}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "440100000000001")
	if got.SourceID != "other" || got.Rules[0].TenantID != 7 {
		t.Errorf("got %+v", got)
	}
	if count(t, mr, "vecgen") != "0" || count(t, mr, "other") != "1" {
		t.Errorf("counts vecgen=%q other=%q", count(t, mr, "vecgen"), count(t, mr, "other"))
	}
	// 同じ取得先への変更では数を変えない
	if err := s.Update(ctx, "440100000000001", Patch{SourceID: &other}); err != nil {
		t.Fatal(err)
	}
	if count(t, mr, "other") != "1" {
		t.Errorf("count other = %q", count(t, mr, "other"))
	}
	if err := s.Update(ctx, "440100000000002", Patch{Rules: &rules}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	if _, _, err := s.SetState(ctx, "440100000000001", StateTerminated); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "440100000000001", Patch{Rules: &rules}); !errors.Is(err, ErrTerminated) {
		t.Errorf("update terminated: %v", err)
	}
}

func TestStateTransitions(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	if err := s.Create(ctx, testSub("440100000000001", "vecgen")); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		to        State
		from      State
		unchanged bool
		err       error
	}{
		{StateActive, StateActive, true, nil},
		{StateInactive, StateActive, false, nil},
		{StateTerminated, StateInactive, false, nil},
		{StateTerminated, StateTerminated, true, nil},
		{StateActive, StateTerminated, false, ErrTransitionNotAllowed},
		{StateInactive, StateTerminated, false, ErrTransitionNotAllowed},
	}
	for _, st := range steps {
		from, unchanged, err := s.SetState(ctx, "440100000000001", st.to)
		if !errors.Is(err, st.err) || from != st.from || unchanged != st.unchanged {
			t.Errorf("to %s: from=%s unchanged=%v err=%v, want %s %v %v", st.to, from, unchanged, err, st.from, st.unchanged, st.err)
		}
	}
	if from, err := s.Reinstate(ctx, "440100000000001"); err != nil || from != StateTerminated {
		t.Fatalf("reinstate: %s %v", from, err)
	}
	if got, _ := s.Get(ctx, "440100000000001"); got.State != StateInactive {
		t.Errorf("state after reinstate = %s", got.State)
	}
	if _, err := s.Reinstate(ctx, "440100000000001"); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Errorf("reinstate inactive: %v", err)
	}
	if _, _, err := s.SetState(ctx, "440100000000002", StateActive); !errors.Is(err, ErrNotFound) {
		t.Errorf("set state missing: %v", err)
	}
	if _, err := s.Reinstate(ctx, "440100000000002"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reinstate missing: %v", err)
	}
}

func TestListAndCounts(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	imsis := []string{"440100000000003", "440100000000001", "440200000000001", "440100000000002"}
	for i, imsi := range imsis {
		src := "vecgen"
		if i%2 == 1 {
			src = "other"
		}
		if err := s.Create(ctx, testSub(imsi, src)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(ctx, "4401", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.NextCursor != "440100000000002" || len(page.Items) != 2 || page.Items[0].IMSI != "440100000000001" {
		t.Fatalf("page 1 = %+v", page)
	}
	page, err = s.List(ctx, "4401", page.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" || len(page.Items) != 1 || page.Items[0].IMSI != "440100000000003" {
		t.Fatalf("page 2 = %+v", page)
	}
	all, _ := s.List(ctx, "", "", 500)
	if all.Total != 4 {
		t.Errorf("total = %d", all.Total)
	}

	got, err := s.GetMany(ctx, []string{"440200000000001", "449999999999999", "440100000000001"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sub := range got {
		ids = append(ids, sub.IMSI)
	}
	if !slices.Equal(ids, []string{"440200000000001", "440100000000001"}) {
		t.Errorf("GetMany = %v", ids)
	}

	counts, err := s.SourceCounts(ctx, []string{"vecgen", "other", "none"})
	if err != nil {
		t.Fatal(err)
	}
	if counts["vecgen"] != 2 || counts["other"] != 2 || counts["none"] != 0 {
		t.Errorf("counts = %v", counts)
	}
}

func TestMatch(t *testing.T) {
	rules := []Rule{
		{TenantID: 1, NasID: "ap-1", SSIDs: []string{"Corp"}, VlanID: 10},
		{TenantID: 1, NasID: "*", SSIDs: []string{"*"}, VlanID: 20},
		{TenantID: 2, NasID: "*", SSIDs: []string{"guest", "corp"}},
	}
	tests := []struct {
		tenant    int64
		nas, ssid string
		want      int
		ok        bool
	}{
		{1, "ap-1", "corp", 0, true}, // SSID は大文字と小文字を区別しない
		{1, "AP-1", "corp", 1, true}, // NAS-Identifier は区別する
		{1, "ap-2", "x", 1, true},
		{2, "ap-1", "CORP", 2, true},
		{2, "ap-1", "other", 0, false},
		{3, "ap-1", "corp", 0, false},
	}
	for _, tt := range tests {
		got, ok := Match(rules, tt.tenant, tt.nas, tt.ssid)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Match(%d,%q,%q) = %d,%v, want %d,%v", tt.tenant, tt.nas, tt.ssid, got, ok, tt.want, tt.ok)
		}
	}
	if SSIDOf("aa-bb-cc-dd-ee-ff:My:SSID") != "My:SSID" || SSIDOf("nocolon") != "nocolon" {
		t.Error("SSIDOf")
	}
}
