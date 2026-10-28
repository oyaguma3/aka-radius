package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRecordAndList(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	var logs bytes.Buffer
	r := NewRecorder(rdb, 100, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx := context.Background()
	for _, a := range []string{"subscriber.create", "subscriber.state", "subscriber.delete"} {
		r.Record(ctx, Entry{Operator: "alice", MgmtClient: "prov", Action: a, Target: "440100123456789", Detail: `{"x":1}`, TraceID: "t1"})
	}
	got, next, err := r.List(ctx, "", 2)
	if err != nil || len(got) != 2 || next == "" || got[0].Action != "subscriber.delete" || got[0].Time.IsZero() || got[0].Detail != `{"x":1}` {
		t.Fatalf("page 1 = %+v %q %v", got, next, err)
	}
	got, next, err = r.List(ctx, next, 2)
	if err != nil || len(got) != 1 || next != "" || got[0].Action != "subscriber.create" {
		t.Fatalf("page 2 = %+v %q %v", got, next, err)
	}
	if _, _, err := r.List(ctx, "bad", 2); err == nil {
		t.Error("bad before accepted")
	}
	if !strings.Contains(logs.String(), `"event_id":"AUDIT"`) {
		t.Error("no AUDIT log")
	}

	// Valkey に書けなくても止まらず、AUDIT_WRITE_ERR を出す
	mr.Close()
	r.Record(ctx, Entry{Action: "subscriber.create"})
	if !strings.Contains(logs.String(), `"event_id":"AUDIT_WRITE_ERR"`) {
		t.Error("no AUDIT_WRITE_ERR log")
	}
}

func TestPreviousID(t *testing.T) {
	for in, want := range map[string]string{"5-3": "5-2", "5-0": "4-18446744073709551615", "0-0": ""} {
		if got, err := previousID(in); err != nil || got != want {
			t.Errorf("previousID(%q) = %q %v", in, got, err)
		}
	}
	if _, err := previousID("x-1"); err == nil {
		t.Error("bad id accepted")
	}
}
