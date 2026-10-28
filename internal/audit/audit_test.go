package audit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRecordAndList(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	var logs bytes.Buffer
	dir := t.TempDir()
	r := NewRecorder(rdb, 100, NewFiles(dir, 14), slog.New(slog.NewJSONHandler(&logs, nil)))
	r.now = func() time.Time { return time.Date(2026, 10, 28, 23, 30, 0, 0, time.UTC) } // 日本時間では 10 月 29 日
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

	// 日ごとのファイル（日本時間の日付）に、Stream と同じ 3 件が書かれている
	lines := readLines(t, filepath.Join(dir, "audit-20261029.jsonl"))
	if len(lines) != 3 || lines[0]["action"] != "subscriber.create" || lines[0]["mgmtClient"] != "prov" ||
		lines[0]["time"] != "2026-10-29T08:30:00+09:00" || lines[0]["streamId"] == "" || lines[0]["traceId"] != "t1" {
		t.Fatalf("file lines = %v", lines)
	}
	if d, _ := lines[0]["detail"].(map[string]any); d["x"] != float64(1) {
		t.Errorf("detail = %v", lines[0]["detail"])
	}

	// Valkey に書けなくても止まらず、AUDIT_WRITE_ERR を出す（ファイルには書く）
	mr.Close()
	r.Record(ctx, Entry{Action: "client.delete"})
	if !strings.Contains(logs.String(), `"event_id":"AUDIT_WRITE_ERR"`) || !strings.Contains(logs.String(), `"to":"stream"`) {
		t.Error("no AUDIT_WRITE_ERR log")
	}
	if lines := readLines(t, filepath.Join(dir, "audit-20261029.jsonl")); len(lines) != 4 || lines[3]["streamId"] != nil {
		t.Errorf("file lines after the stream failure = %v", lines)
	}
}

func TestRecordFileError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	var logs bytes.Buffer
	r := NewRecorder(rdb, 100, NewFiles(filepath.Join(t.TempDir(), "missing"), 14), slog.New(slog.NewJSONHandler(&logs, nil)))
	r.Record(t.Context(), Entry{Action: "client.create"})
	if !strings.Contains(logs.String(), `"to":"file"`) {
		t.Errorf("log = %s", logs.String())
	}
	if NewFiles("", 14) != nil {
		t.Error(`NewFiles("") should be nil`)
	}
}

func TestRecordWithoutValkey(t *testing.T) {
	// Valkey につながない Recorder は、日ごとのファイルにだけ書く
	var logs bytes.Buffer
	dir := t.TempDir()
	r := NewRecorder(nil, 100, NewFiles(dir, 14), slog.New(slog.NewJSONHandler(&logs, nil)))
	r.Record(t.Context(), Entry{Operator: "cli", MgmtClient: "cli", Action: "admin-server-certificate.create", Target: "aka-radius-admin"})
	paths, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(paths) != 1 {
		t.Fatalf("files = %v", paths)
	}
	if lines := readLines(t, paths[0]); len(lines) != 1 || lines[0]["action"] != "admin-server-certificate.create" || lines[0]["streamId"] != nil {
		t.Errorf("lines = %v", lines)
	}
	if strings.Contains(logs.String(), "AUDIT_WRITE_ERR") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestCheckWritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	if err := CheckWritableDir(dir); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("left files: %v", ents)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritableDir(file); err == nil {
		t.Error("CheckWritableDir on a file: want error")
	}
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	}
	var out []map[string]any
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestMaintain(t *testing.T) {
	dir := t.TempDir()
	f := NewFiles(dir, 14)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, jst)
	write := func(name, content string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write("audit-20261028.jsonl", `{"a":1}`+"\n", now)                       // 今日: そのまま
	write("audit-20261027.jsonl", `{"a":2}`+"\n", now.Add(-time.Hour))       // 昨日: 圧縮する
	write("audit-20261026.jsonl", `{"a":3}`+"\n", now.Add(-5*time.Minute))   // 書いたばかり: まだ圧縮しない
	write("audit-20261014.jsonl.gz", "old", now.Add(-14*24*time.Hour))       // 14 日前: 残す
	write("audit-20261013.jsonl.gz", "older", now.Add(-15*24*time.Hour))     // 15 日前: 消す
	write("audit-20261001.jsonl", `{"a":4}`+"\n", now.Add(-27*24*time.Hour)) // 古い圧縮前: 消す
	write("other.txt", "x", now.Add(-30*24*time.Hour))                       // 関係のないファイル: 触らない

	if err := f.Maintain(now); err != nil {
		t.Fatal(err)
	}
	var names []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		names = append(names, e.Name())
	}
	want := []string{"audit-20261014.jsonl.gz", "audit-20261026.jsonl", "audit-20261027.jsonl.gz", "audit-20261028.jsonl", "other.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v", names, want)
	}
	if lines := readLines(t, filepath.Join(dir, "audit-20261027.jsonl.gz")); len(lines) != 1 || lines[0]["a"] != float64(2) {
		t.Errorf("compressed content = %v", lines)
	}
	if err := NewFiles(filepath.Join(dir, "none"), 14).Maintain(now); err == nil {
		t.Error("Maintain on a missing directory: want error")
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
