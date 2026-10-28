package auth

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/auth/source/sourcetest"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	"github.com/redis/go-redis/v9"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestReloadSources(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	registry, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	subs := subscriber.NewStore(rdb)

	t.Run("読み直しの成功", func(t *testing.T) {
		logs := captureLogs(t)
		reloadSources(registry, subs)
		out := logs.String()
		for _, want := range []string{`"event_id":"SOURCES_RELOADED"`, `"id":"vecgen"`, `"timeout_ms":1000`, `"client_cert_expires"`} {
			if !strings.Contains(out, want) {
				t.Errorf("log does not contain %s: %s", want, out)
			}
		}
		// 鍵のパスは出さない
		if strings.Contains(out, "client.key") {
			t.Errorf("log contains the key path: %s", out)
		}
	})

	t.Run("読み直しの失敗では前の登録簿のまま", func(t *testing.T) {
		if err := os.WriteFile(fake.RegistryFile, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t)
		reloadSources(registry, subs)
		if !strings.Contains(logs.String(), `"event_id":"SOURCES_RELOAD_ERR"`) {
			t.Errorf("log does not contain SOURCES_RELOAD_ERR: %s", logs.String())
		}
		if _, ok := registry.Get("vecgen"); !ok {
			t.Error("the previous registry was dropped")
		}
	})

	t.Run("加入者が指している取得先は消さない", func(t *testing.T) {
		if err := os.WriteFile(fake.RegistryFile, []byte(`{"sources":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		mr.Set(subscriber.SourceCountKey("vecgen"), "3")
		logs := captureLogs(t)
		reloadSources(registry, subs)
		if out := logs.String(); !strings.Contains(out, `"event_id":"SOURCES_RELOAD_ERR"`) || !strings.Contains(out, `used by 3 subscribers`) {
			t.Errorf("log = %s", out)
		}
		if _, ok := registry.Get("vecgen"); !ok {
			t.Error("a source in use was removed")
		}
		mr.Set(subscriber.SourceCountKey("vecgen"), "0")
	})

	t.Run("空の登録簿は WARN", func(t *testing.T) {
		if err := os.WriteFile(fake.RegistryFile, []byte(`{"sources":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t)
		reloadSources(registry, subs)
		out := logs.String()
		if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"count":0`) {
			t.Errorf("log = %s, want a WARN with count 0", out)
		}
	})
}
