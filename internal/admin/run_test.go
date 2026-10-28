package admin

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.json")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","baseUrl":"https://h/nudm-ueau/v1","clientCertFile":"c.pem","clientKeyFile":"c.key","serverCaFile":"ca.pem"}`
	}
	write(`{"sources":[` + entry("vecgen") + `]}`)
	s, err := loadSources(path)
	if err != nil {
		t.Fatal(err)
	}
	infos, at := s.Infos()
	if len(infos) != 1 || infos[0].ID != "vecgen" || at.IsZero() {
		t.Fatalf("infos = %+v", infos)
	}
	// 形が違えば前のまま
	write(`{`)
	if err := s.reload(); err == nil {
		t.Error("bad file accepted")
	}
	if infos, _ := s.Infos(); len(infos) != 1 {
		t.Errorf("infos after a failed reload = %+v", infos)
	}
	write(`{"sources":[` + entry("vecgen") + `,` + entry("other") + `]}`)
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logSources(slog.New(slog.NewJSONHandler(&logs, nil)), "reloaded", "SOURCES_RELOADED", s)
	if !strings.Contains(logs.String(), `"ids":["vecgen","other"]`) {
		t.Errorf("log = %s", logs.String())
	}
	if _, err := loadSources(filepath.Join(dir, "none.json")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestRecoverer(t *testing.T) {
	var logs bytes.Buffer
	h := recoverer(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/v2/status", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "SYSTEM_FAILURE") {
		t.Errorf("response = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), `"event_id":"SYS_PANIC"`) {
		t.Errorf("log = %s", logs.String())
	}
}
