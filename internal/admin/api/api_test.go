package api

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/audit"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/clients"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	"github.com/oyaguma3/aka-radius/internal/usage"
	"github.com/redis/go-redis/v9"
)

const testIMSI = "440100123456789"

type fakeSources struct{ infos []source.Info }

func (f fakeSources) Infos() ([]source.Info, time.Time) {
	return f.infos, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
}

type testAPI struct {
	srv  *httptest.Server
	mr   *miniredis.Miniredis
	rdb  *redis.Client
	logs *bytes.Buffer
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := &Handler{
		Subs:     subscriber.NewStore(rdb),
		Sessions: sessions.NewStore(rdb),
		Usage:    usage.NewStore(rdb),
		Sources: fakeSources{infos: []source.Info{
			{ID: "vecgen", Name: "aka-vecgen", BaseURL: "https://aka-vecgen:8443/nudm-ueau/v1", Timeout: 3 * time.Second, NotAfter: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC),
				ClientCertSubject: "CN=aka-radius", ClientCertFingerprint: strings.Repeat("ab", 32),
				ServerCAs: []source.CertInfo{{Subject: "CN=aka-vecgen", NotAfter: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}}},
			{ID: "other", Name: "other", BaseURL: "https://other/nudm-ueau/v1", Timeout: time.Second},
		}},
		Clients:      clients.NewStore(rdb),
		SourceStatus: source.NewStatusStore(rdb),
		Audit:        audit.NewRecorder(rdb, 1000, nil, log),
		Log:          log,
		MgmtClient:   func(*http.Request) string { return "provisioner" },
		AdminClients: func() ([]string, time.Time) {
			return []string{"provisioner"}, time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
		},
		Build:           "test",
		NodeName:        "node-1",
		StartedAt:       time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC),
		StampVendorID:   32473,
		StampVendorType: 1,
	}
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return &testAPI{srv: srv, mr: mr, rdb: rdb, logs: &logs}
}

// do は要求を送り、状態の符号と本文（JSON をデコードしたもの）を返す。
func (a *testAPI) do(t *testing.T, method, path, body string, headers ...string) (int, map[string]any, http.Header) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, r)
	req.Header.Set("X-Operator-Id", "alice")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode, out, resp.Header
}

func (a *testAPI) wantStatus(t *testing.T, method, path, body string, status int, cause string) map[string]any {
	t.Helper()
	got, out, _ := a.do(t, method, path, body)
	if got != status || (cause != "" && out["cause"] != cause) {
		t.Fatalf("%s %s %s: status=%d body=%v, want %d %s", method, path, body, got, out, status, cause)
	}
	return out
}

const createBody = `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen",
  "rules":[{"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":100,"sessionTimeout":3600}]}`

func TestSubscriberLifecycle(t *testing.T) {
	a := newTestAPI(t)
	base := "/admin/v2/subscribers"

	code, out, hdr := a.do(t, "POST", base, createBody)
	if code != http.StatusCreated || out["state"] != "active" || out["sourceId"] != "vecgen" || hdr.Get("Location") != base+"/"+testIMSI {
		t.Fatalf("create: %d %v", code, out)
	}
	rules := out["rules"].([]any)
	if r := rules[0].(map[string]any); r["tenantId"] != float64(42) || r["vlanId"] != float64(100) {
		t.Errorf("rules = %v", rules)
	}
	a.wantStatus(t, "POST", base, createBody, http.StatusConflict, causeSubscriberExists)
	a.wantStatus(t, "GET", base+"/"+testIMSI, "", http.StatusOK, "")
	a.wantStatus(t, "GET", base+"/440100123456780", "", http.StatusNotFound, causeUserNotFound)
	a.wantStatus(t, "GET", base+"/abc", "", http.StatusNotFound, causeUserNotFound)

	// 変更（取得先とルール）。状態は専用の口
	out = a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"sourceId":"other","rules":[{"tenantId":7,"nasId":"ap-1","ssids":["corp","guest"]}]}`, http.StatusOK, "")
	if out["sourceId"] != "other" || len(out["rules"].([]any)) != 1 {
		t.Errorf("patched = %v", out)
	}
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"state":"inactive"}`, http.StatusBadRequest, causeOptionalIEIncorrect)
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"sourceId":"nowhere"}`, http.StatusBadRequest, causeUnknownSource)
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"x":1}`, http.StatusBadRequest, causeOptionalIEIncorrect)
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{}`, http.StatusBadRequest, causeMandatoryIEMissing)
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"rules":[{"tenantId":1,"nasId":"*","ssids":["*"],"vlan":1}]}`, http.StatusBadRequest, causeOptionalIEIncorrect)

	// 状態: active は削除できない → inactive → terminated（変更できない）→ reinstate → 削除
	a.wantStatus(t, "DELETE", base+"/"+testIMSI, "", http.StatusConflict, causeSubscriberActive)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"inactive","reason":"test"}`, http.StatusOK, "")
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"inactive"}`, http.StatusOK, "") // 同じ状態
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"terminated"}`, http.StatusOK, "")
	a.wantStatus(t, "PATCH", base+"/"+testIMSI, `{"sourceId":"vecgen"}`, http.StatusConflict, causeSubscriberTerminated)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"active"}`, http.StatusConflict, causeTransitionNotAllowed)
	a.wantStatus(t, "POST", base+"/"+testIMSI+"/reinstate", `{}`, http.StatusBadRequest, causeMandatoryIEMissing)
	out = a.wantStatus(t, "POST", base+"/"+testIMSI+"/reinstate", `{"reason":"mistake"}`, http.StatusOK, "")
	if out["state"] != "inactive" {
		t.Errorf("reinstated = %v", out)
	}
	a.wantStatus(t, "POST", base+"/"+testIMSI+"/reinstate", `{"reason":"again"}`, http.StatusConflict, causeTransitionNotAllowed)
	code, _, _ = a.do(t, "DELETE", base+"/"+testIMSI, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	a.wantStatus(t, "DELETE", base+"/"+testIMSI, "", http.StatusNotFound, causeUserNotFound)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"active"}`, http.StatusNotFound, causeUserNotFound)
	a.wantStatus(t, "POST", base+"/"+testIMSI+"/reinstate", `{"reason":"x"}`, http.StatusNotFound, causeUserNotFound)

	// 監査ログ（新しい順）
	out = a.wantStatus(t, "GET", "/admin/v2/audit-logs?limit=500", "", http.StatusOK, "")
	var actions []string
	for _, it := range out["items"].([]any) {
		e := it.(map[string]any)
		actions = append(actions, e["action"].(string))
		if e["operator"] != "alice" || e["mgmtClient"] != "provisioner" || e["target"] != testIMSI || e["traceId"] == "" {
			t.Errorf("entry = %v", e)
		}
	}
	want := "subscriber.delete subscriber.reinstate subscriber.state subscriber.state subscriber.state subscriber.update subscriber.create"
	if strings.Join(actions, " ") != want {
		t.Errorf("actions = %v", actions)
	}
	// 同じ状態への変更は unchanged を残す
	if !strings.Contains(a.logs.String(), `"event_id":"AUDIT"`) {
		t.Error("no AUDIT log")
	}
}

func TestCreateValidation(t *testing.T) {
	a := newTestAPI(t)
	base := "/admin/v2/subscribers"
	tests := []struct {
		name, body, cause string
	}{
		{"JSON でない", `{`, causeInvalidMsgFormat},
		{"未知の項目", `{"imsi":"` + testIMSI + `","x":1}`, causeInvalidMsgFormat},
		{"ルールの未知の項目", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*"],"x":1}]}`, causeInvalidMsgFormat},
		{"必須の欠落", `{"imsi":"` + testIMSI + `"}`, causeMandatoryIEMissing},
		{"IMSI の形", `{"imsi":"123","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"terminated で登録", `{"imsi":"` + testIMSI + `","state":"terminated","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"取得先の ID の形", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"BAD","rules":[{"tenantId":1,"nasId":"*","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"ルールがない", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[]}`, causeMandatoryIEIncorrect},
		{"テナントが 0", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":0,"nasId":"*","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"テナントがない", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"nasId":"*","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"NAS-Identifier が空", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"","ssids":["*"]}]}`, causeMandatoryIEIncorrect},
		{"SSID の * と名前の混在", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*","corp"]}]}`, causeMandatoryIEIncorrect},
		{"SSID が長い", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["` + strings.Repeat("a", 33) + `"]}]}`, causeMandatoryIEIncorrect},
		{"VLAN の範囲", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*"],"vlanId":4095}]}`, causeMandatoryIEIncorrect},
		{"Session-Timeout の範囲", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"vecgen","rules":[{"tenantId":1,"nasId":"*","ssids":["*"],"sessionTimeout":0}]}`, causeMandatoryIEIncorrect},
		{"登録簿にない取得先", `{"imsi":"` + testIMSI + `","state":"active","sourceId":"nowhere","rules":[{"tenantId":1,"nasId":"*","ssids":["*"]}]}`, causeUnknownSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.wantStatus(t, "POST", base, tt.body, http.StatusBadRequest, tt.cause)
		})
	}
	// 33 件のルール
	var rules []string
	for range 33 {
		rules = append(rules, `{"tenantId":1,"nasId":"*","ssids":["*"]}`)
	}
	a.wantStatus(t, "POST", base, `{"imsi":"`+testIMSI+`","state":"active","sourceId":"vecgen","rules":[`+strings.Join(rules, ",")+`]}`, http.StatusBadRequest, causeMandatoryIEIncorrect)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"paused"}`, http.StatusBadRequest, causeMandatoryIEIncorrect)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{"state":"active","reason":"`+strings.Repeat("x", 501)+`"}`, http.StatusBadRequest, causeOptionalIEIncorrect)
	a.wantStatus(t, "PUT", base+"/"+testIMSI+"/state", `{}`, http.StatusBadRequest, causeMandatoryIEMissing)
}

func TestListAndBatchGet(t *testing.T) {
	a := newTestAPI(t)
	for _, imsi := range []string{"440100000000003", "440100000000001", "440200000000001", "440100000000002"} {
		a.wantStatus(t, "POST", "/admin/v2/subscribers", strings.Replace(createBody, testIMSI, imsi, 1), http.StatusCreated, "")
	}
	out := a.wantStatus(t, "GET", "/admin/v2/subscribers?prefix=4401&limit=2", "", http.StatusOK, "")
	if out["total"] != float64(3) || out["nextCursor"] != "440100000000002" || len(out["items"].([]any)) != 2 {
		t.Fatalf("page 1 = %v", out)
	}
	out = a.wantStatus(t, "GET", "/admin/v2/subscribers?prefix=4401&limit=2&cursor=440100000000002", "", http.StatusOK, "")
	if _, ok := out["nextCursor"]; ok || len(out["items"].([]any)) != 1 {
		t.Fatalf("page 2 = %v", out)
	}
	a.wantStatus(t, "GET", "/admin/v2/subscribers?prefix=abc", "", http.StatusBadRequest, causeInvalidQueryParam)
	a.wantStatus(t, "GET", "/admin/v2/subscribers?cursor=1", "", http.StatusBadRequest, causeInvalidQueryParam)
	a.wantStatus(t, "GET", "/admin/v2/subscribers?limit=501", "", http.StatusBadRequest, causeInvalidQueryParam)

	out = a.wantStatus(t, "POST", "/admin/v2/subscribers/batch-get", `{"imsis":["440200000000001","449999999999999","440200000000001","440100000000001"]}`, http.StatusOK, "")
	items := out["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["imsi"] != "440200000000001" || len(out["notFound"].([]any)) != 1 {
		t.Errorf("batch-get = %v", out)
	}
	a.wantStatus(t, "POST", "/admin/v2/subscribers/batch-get", `{}`, http.StatusBadRequest, causeMandatoryIEMissing)
	a.wantStatus(t, "POST", "/admin/v2/subscribers/batch-get", `{"imsis":[]}`, http.StatusBadRequest, causeMandatoryIEIncorrect)
	a.wantStatus(t, "POST", "/admin/v2/subscribers/batch-get", `{"imsis":["1"]}`, http.StatusBadRequest, causeMandatoryIEIncorrect)

	// 本PoC の形の sub: が残っていれば 500（移し替えない）
	a.mr.HSet("sub:440300000000001", "ki", "00")
	a.wantStatus(t, "POST", "/admin/v2/subscribers/batch-get", `{"imsis":["440300000000001"]}`, http.StatusInternalServerError, causeSystemFailure)
}

func TestStatusAndCommon(t *testing.T) {
	a := newTestAPI(t)
	a.wantStatus(t, "POST", "/admin/v2/subscribers", createBody, http.StatusCreated, "")
	out := a.wantStatus(t, "GET", "/admin/v2/status", "", http.StatusOK, "")
	if out["version"] != Version || out["build"] != "test" || out["subscriberCount"] != float64(1) || out["stampVendorId"] != float64(32473) {
		t.Errorf("status = %v", out)
	}
	srcs := out["sources"].([]any)
	if s := srcs[0].(map[string]any); s["id"] != "vecgen" || s["subscriberCount"] != float64(1) || s["clientCertificateNotAfter"] == nil || s["timeoutMs"] != float64(3000) {
		t.Errorf("sources[0] = %v", s)
	}
	if s := srcs[1].(map[string]any); s["subscriberCount"] != float64(0) || s["clientCertificateNotAfter"] != nil {
		t.Errorf("sources[1] = %v", s)
	}
	// 遮断器の報告がまだなければ circuitBreaker は返さない。RADIUS クライアントの数と管理クライアントを読んだ時刻
	if s := srcs[0].(map[string]any); s["circuitBreaker"] != nil || out["clientCount"] != float64(0) || out["adminClientsLoadedAt"] != "2026-10-28T02:00:00Z" {
		t.Errorf("status = %v", out)
	}

	// X-Trace-ID は受け取った値をそのまま返し、なければ採番する
	_, _, hdr := a.do(t, "GET", "/admin/v2/status", "", "X-Trace-ID", "trace-123")
	if hdr.Get("X-Trace-ID") != "trace-123" {
		t.Errorf("X-Trace-ID = %q", hdr.Get("X-Trace-ID"))
	}
	_, _, hdr = a.do(t, "GET", "/admin/v2/status", "")
	if len(hdr.Get("X-Trace-ID")) != 32 {
		t.Errorf("generated X-Trace-ID = %q", hdr.Get("X-Trace-ID"))
	}
	code, out, _ := a.do(t, "GET", "/admin/v2/status", "", "X-Operator-Id", "bad id!")
	if code != http.StatusBadRequest || out["cause"] != causeOptionalIEIncorrect {
		t.Errorf("bad operator: %d %v", code, out)
	}
	a.wantStatus(t, "GET", "/admin/v1/subscribers", "", http.StatusNotFound, causeNotFound)
	if !strings.Contains(a.logs.String(), `"event_id":"ADMIN_REQUEST"`) {
		t.Error("no ADMIN_REQUEST log")
	}

	// 監査ログのページ送り
	for range 3 {
		a.wantStatus(t, "PUT", "/admin/v2/subscribers/"+testIMSI+"/state", `{"state":"active"}`, http.StatusOK, "")
	}
	out = a.wantStatus(t, "GET", "/admin/v2/audit-logs?limit=2", "", http.StatusOK, "")
	next, _ := out["nextBefore"].(string)
	if len(out["items"].([]any)) != 2 || next == "" {
		t.Fatalf("audit page 1 = %v", out)
	}
	out = a.wantStatus(t, "GET", "/admin/v2/audit-logs?limit=2&before="+next, "", http.StatusOK, "")
	if len(out["items"].([]any)) != 2 {
		t.Errorf("audit page 2 = %v", out)
	}
	if d := out["items"].([]any)[0].(map[string]any)["detail"].(map[string]any); d["unchanged"] != true {
		t.Errorf("detail = %v", d)
	}
	a.wantStatus(t, "GET", "/admin/v2/audit-logs?before=x", "", http.StatusBadRequest, causeInvalidQueryParam)
}

func TestClientsAndSources(t *testing.T) {
	a := newTestAPI(t)
	cs := clients.NewStore(a.rdb)
	secret := strings.Repeat("s", 16)
	for _, c := range []clients.Client{
		{IP: "10.0.0.10", Name: "edge-02", Secret: secret, TrustStamp: true},
		{IP: "10.0.0.9", Name: "edge-01", Secret: secret},
	} {
		if err := cs.Add(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := a.do(t, "GET", "/admin/v2/clients", "")
	items, _ := out["items"].([]any)
	if code != http.StatusOK || len(items) != 2 {
		t.Fatalf("clients = %d %v", code, out)
	}
	first := items[0].(map[string]any)
	if first["ip"] != "10.0.0.9" || first["name"] != "edge-01" || first["trustStamp"] != false || first["createdAt"] == nil {
		t.Errorf("clients[0] = %v", first)
	}
	if items[1].(map[string]any)["trustStamp"] != true {
		t.Errorf("clients[1] = %v", items[1])
	}
	// 共有シークレットは返さない
	_, raw := a.raw(t, "/admin/v2/clients")
	if strings.Contains(raw, secret) || strings.Contains(raw, "secret") {
		t.Errorf("clients response contains the secret: %s", raw)
	}
	if out := a.wantStatus(t, "GET", "/admin/v2/status", "", http.StatusOK, ""); out["clientCount"] != float64(2) {
		t.Errorf("clientCount = %v", out["clientCount"])
	}

	// auth の遮断器の報告
	reported := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	if err := source.NewStatusStore(a.rdb).Write(t.Context(), source.Status{ReportedAt: reported, Sources: map[string]source.BreakerStatus{
		"vecgen": {State: "open", Since: reported.Add(-time.Minute)},
	}}); err != nil {
		t.Fatal(err)
	}
	a.wantStatus(t, "POST", "/admin/v2/subscribers", createBody, http.StatusCreated, "")
	out = a.wantStatus(t, "GET", "/admin/v2/sources", "", http.StatusOK, "")
	srcs := out["items"].([]any)
	s0 := srcs[0].(map[string]any)
	cb, _ := s0["circuitBreaker"].(map[string]any)
	if s0["id"] != "vecgen" || s0["subscriberCount"] != float64(1) || cb["state"] != "open" || cb["reportedAt"] != "2026-10-28T03:00:00Z" || cb["since"] != "2026-10-28T02:59:00Z" {
		t.Errorf("sources[0] = %v", s0)
	}
	cert, _ := s0["clientCertificate"].(map[string]any)
	cas, _ := s0["serverCas"].([]any)
	if cert["fingerprint"] != strings.Repeat("ab", 32) || cert["subject"] != "CN=aka-radius" || cert["notAfter"] != "2029-01-01T00:00:00Z" ||
		len(cas) != 1 || cas[0].(map[string]any)["notAfter"] != "2030-01-01T00:00:00Z" {
		t.Errorf("certificates = %v %v", cert, cas)
	}
	if s1 := srcs[1].(map[string]any); s1["clientCertificate"] != nil || s1["circuitBreaker"] != nil || len(s1["serverCas"].([]any)) != 0 {
		t.Errorf("sources[1] = %v", s1)
	}
	if out["loadedAt"] != "2026-10-28T00:00:00Z" {
		t.Errorf("loadedAt = %v", out["loadedAt"])
	}
	st := a.wantStatus(t, "GET", "/admin/v2/status", "", http.StatusOK, "")
	if cb := st["sources"].([]any)[0].(map[string]any)["circuitBreaker"].(map[string]any); cb["state"] != "open" {
		t.Errorf("status circuitBreaker = %v", cb)
	}
}

// raw は GET の本文をそのまま返す。
func (a *testAPI) raw(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(a.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPreviousIDBoundary(t *testing.T) {
	a := newTestAPI(t)
	out := a.wantStatus(t, "GET", "/admin/v2/audit-logs?before=0-0", "", http.StatusOK, "")
	if len(out["items"].([]any)) != 0 {
		t.Errorf("items = %v", out)
	}
}

func TestSessions(t *testing.T) {
	a := newTestAPI(t)
	st := sessions.NewStore(a.rdb)
	now := time.Now()
	for i, x := range []struct {
		id, imsi string
		tenant   int64
	}{{"s1", "440100000000001", 42}, {"s2", "440100000000002", 42}, {"s3", "440100000000001", 7}} {
		if err := st.Create(t.Context(), sessions.Session{ID: x.id, IMSI: x.imsi, TenantID: x.tenant, NasIP: "192.0.2.1", NasIdentifier: "ap"}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	out := a.wantStatus(t, "GET", "/admin/v2/sessions?tenantId=42&limit=1", "", http.StatusOK, "")
	items := out["items"].([]any)
	if len(items) != 1 || out["nextCursor"] != "1" || items[0].(map[string]any)["id"] != "s1" || items[0].(map[string]any)["tenantId"] != float64(42) {
		t.Fatalf("page 1 = %v", out)
	}
	out = a.wantStatus(t, "GET", "/admin/v2/sessions?tenantId=42&limit=1&cursor=1", "", http.StatusOK, "")
	if items := out["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != "s2" || out["nextCursor"] != nil {
		t.Fatalf("page 2 = %v", out)
	}
	out = a.wantStatus(t, "GET", "/admin/v2/sessions?imsi=440100000000001,440100000000009", "", http.StatusOK, "")
	if items := out["items"].([]any); len(items) != 2 {
		t.Fatalf("imsi = %v", out)
	}
	for _, q := range []string{"", "tenantId=1&imsi=440100000000001", "tenantId=x", "imsi=123", "tenantId=1&cursor=x", "tenantId=1&limit=1001",
		"imsi=" + strings.TrimSuffix(strings.Repeat("440100000000001,", 101), ",")} {
		a.wantStatus(t, "GET", "/admin/v2/sessions?"+q, "", http.StatusBadRequest, causeInvalidQueryParam)
	}

	// /status のセッションの数は、acct の掃除で数えた後だけ返す
	if out = a.wantStatus(t, "GET", "/admin/v2/status", "", http.StatusOK, ""); out["sessionCount"] != nil {
		t.Errorf("sessionCount before sweep = %v", out["sessionCount"])
	}
	if _, err := st.Sweep(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if out = a.wantStatus(t, "GET", "/admin/v2/status", "", http.StatusOK, ""); out["sessionCount"] != float64(3) || out["sessionsCountedAt"] == nil {
		t.Errorf("sessionCount = %v", out["sessionCount"])
	}
}

func TestUsage(t *testing.T) {
	a := newTestAPI(t)
	us := usage.NewStore(a.rdb)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, usage.JST)
	// 期限（EXPIREAT）を固定の時刻で付けるので、miniredis の時計も同じ時刻に合わせる
	// （合わせないと、実際の日付が進むと書いたキーがすぐに期限切れになる）。
	a.mr.SetTime(now)
	for _, imsi := range []string{"440100000000002", "440100000000001"} {
		if err := us.AddAuthOK(t.Context(), now, 42, imsi); err != nil {
			t.Fatal(err)
		}
	}
	a.mr.HSet(usage.Key("20261028", 42, "440100000000001"), usage.FieldSeconds, "60", usage.FieldInOctets, "100")
	out := a.wantStatus(t, "GET", "/admin/v2/usage?date=2026-10-28&limit=1", "", http.StatusOK, "")
	items := out["items"].([]any)
	if len(items) != 1 || out["nextCursor"] != "42:440100000000001" {
		t.Fatalf("page 1 = %v", out)
	}
	if r := items[0].(map[string]any); r["date"] != "2026-10-28" || r["tenantId"] != float64(42) || r["authOk"] != float64(1) || r["seconds"] != float64(60) || r["inOctets"] != float64(100) {
		t.Errorf("record = %v", r)
	}
	out = a.wantStatus(t, "GET", "/admin/v2/usage?date=2026-10-28&cursor=42:440100000000001", "", http.StatusOK, "")
	if items := out["items"].([]any); len(items) != 1 || items[0].(map[string]any)["imsi"] != "440100000000002" {
		t.Fatalf("page 2 = %v", out)
	}
	if out = a.wantStatus(t, "GET", "/admin/v2/usage?date=2026-10-27", "", http.StatusOK, ""); len(out["items"].([]any)) != 0 {
		t.Errorf("other day = %v", out)
	}
	for _, q := range []string{"", "date=20261028", "date=2026-10-28&cursor=x", "date=2026-10-28&limit=0"} {
		a.wantStatus(t, "GET", "/admin/v2/usage?"+q, "", http.StatusBadRequest, causeInvalidQueryParam)
	}
}
