package source_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/auth/source/sourcetest"
	eapaka "github.com/oyaguma3/go-eapaka"
)

const testIMSI = "001010000000001"

// fakeLookup は試験用の SubscriberLookup（IMSI → 取得先の ID）。
type fakeLookup struct {
	ids map[string]string
	err error
}

func (f fakeLookup) GetSourceID(_ context.Context, imsi string) (string, error) {
	return f.ids[imsi], f.err
}

// newService は偽の取得先 1 つと、testIMSI がそれを指す Service を作る。
func newService(t *testing.T) (*source.Service, *sourcetest.Server, *source.Registry) {
	t.Helper()
	fake := sourcetest.Start(t, "vecgen")
	reg, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return source.NewService(fakeLookup{ids: map[string]string{testIMSI: "vecgen"}}, reg), fake, reg
}

func wantKind(t *testing.T, err error, kind source.Kind) *source.Error {
	t.Helper()
	var se *source.Error
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *source.Error", err)
	}
	if se.Kind != kind {
		t.Fatalf("kind = %s, want %s (%v)", se.Kind, kind, err)
	}
	return se
}

func TestFetch_AKA(t *testing.T) {
	svc, fake, _ := newService(t)
	v, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA, ANID: "WLAN", TraceID: "trace-1"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sent := fake.Vectors()[0]
	if !bytes.Equal(v.RAND, sent.RAND) || !bytes.Equal(v.XRES, sent.XRES) || !bytes.Equal(v.CK, sent.CK) || !bytes.Equal(v.IK, sent.IK) || v.SourceID != "vecgen" {
		t.Errorf("vector = %+v, want the one the source returned", v)
	}
	req := fake.Requests()[0]
	if req.IMSI != testIMSI || req.PathType != "eap-aka" || req.TraceID != "trace-1" {
		t.Errorf("request = %+v", req)
	}
	if req.Body["hssAuthType"] != "EAP_AKA" || req.Body["numOfRequestedVectors"] != float64(1) {
		t.Errorf("body = %v", req.Body)
	}
	if _, ok := req.Body["anId"]; ok {
		t.Error("anId must not be sent for EAP-AKA")
	}
}

func TestFetch_AKAPrime(t *testing.T) {
	// EAP-AKA' は eap-aka-prime と anId で要求し、取得先が導出した CK'・IK' を受け取る（契約 §6.4）。
	svc, fake, _ := newService(t)
	v, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKAPrime, ANID: "WLAN"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sent := fake.Vectors()[0]
	ckp, ikp, err := eapaka.DeriveCKPrimeIKPrime(sent.CK, sent.IK, "WLAN", sent.AUTN)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.CK, ckp) || !bytes.Equal(v.IK, ikp) {
		t.Error("CK/IK of the vector are not the CK'/IK' the source derived")
	}
	req := fake.Requests()[0]
	if req.PathType != "eap-aka-prime" || req.Body["hssAuthType"] != "EAP_AKA_PRIME" || req.Body["anId"] != "WLAN" {
		t.Errorf("request = %+v", req)
	}
}

func TestFetch_Resync(t *testing.T) {
	svc, fake, _ := newService(t)
	rnd := bytes.Repeat([]byte{0x11}, 16)
	auts := bytes.Repeat([]byte{0x22}, 14)
	if _, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, SourceID: "vecgen", Method: source.MethodAKA,
		Resync: &source.Resync{RAND: rnd, AUTS: auts}}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ri, _ := fake.Requests()[0].Body["resynchronizationInfo"].(map[string]any)
	if ri["rand"] != hex.EncodeToString(rnd) || ri["auts"] != hex.EncodeToString(auts) {
		t.Errorf("resynchronizationInfo = %v", ri)
	}
}

func TestFetch_SourceUnknown(t *testing.T) {
	// 取得先が決まらなければ、取得先を呼ばない。
	svc, fake, _ := newService(t)
	_, err := svc.Fetch(context.Background(), &source.Request{IMSI: "001010000000002", Method: source.MethodAKA})
	wantKind(t, err, source.KindSourceUnknown)
	_, err = svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, SourceID: "nowhere", Method: source.MethodAKA})
	if se := wantKind(t, err, source.KindSourceUnknown); se.SourceID != "nowhere" {
		t.Errorf("SourceID = %q", se.SourceID)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("source was called %d times", n)
	}
}

func TestFetch_LookupFailed(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	reg, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	svc := source.NewService(fakeLookup{err: errors.New("valkey down")}, reg)
	_, err = svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
	wantKind(t, err, source.KindLookupFailed)
}

func TestFetch_Classification(t *testing.T) {
	// 契約 §9.3 の分類。
	vector := func(item string) string { return `{"hssAuthenticationVectors":[` + item + `]}` }
	good := `{"avType":"EAP_AKA","rand":"` + strings.Repeat("01", 16) + `","autn":"` + strings.Repeat("02", 16) +
		`","xres":"` + strings.Repeat("03", 8) + `","ck":"` + strings.Repeat("04", 16) + `","ik":"` + strings.Repeat("05", 16) + `"}`
	tests := []struct {
		name       string
		status     int
		ctype      string
		body       string
		wantKind   source.Kind
		wantCause  string
		wantReason string
	}{
		{"一般的な拒否", 403, "application/problem+json", `{"title":"Forbidden","status":403,"cause":"AUTHENTICATION_REJECTED"}`, source.KindRejected, "AUTHENTICATION_REJECTED", ""},
		{"許していない anId", 400, "application/problem+json", `{"status":400,"cause":"OPTIONAL_IE_INCORRECT"}`, source.KindRejected, "OPTIONAL_IE_INCORRECT", ""},
		{"対応しない認証タイプ", 501, "application/problem+json", `{"status":501,"cause":"AUTH_TYPE_NOT_SUPPORTED"}`, source.KindRejected, "AUTH_TYPE_NOT_SUPPORTED", ""},
		{"中の誤り", 500, "application/problem+json", `{"status":500,"cause":"SYSTEM_FAILURE"}`, source.KindTemporary, "SYSTEM_FAILURE", ""},
		{"混み合い", 503, "", ``, source.KindTemporary, "", ""},
		{"本文のない 403", 403, "", ``, source.KindContractViolation, "", "error without problem details"},
		{"cause のない 400", 400, "application/problem+json", `{"status":400}`, source.KindContractViolation, "", "error without problem details"},
		{"契約にない 404", 404, "application/problem+json", `{"status":404,"cause":"USER_NOT_FOUND"}`, source.KindContractViolation, "", "status not in contract"},
		{"契約にない 502", 502, "", ``, source.KindContractViolation, "", "status not in contract"},
		{"JSON でない 200", 200, "application/json", `not json`, source.KindContractViolation, "", "invalid json"},
		{"ベクターが 2 つ", 200, "application/json", vector(good + "," + good), source.KindContractViolation, "", "want 1 vector, got 2"},
		{"avType が違う", 200, "application/json", vector(strings.Replace(good, `"EAP_AKA"`, `"EAP_AKA_PRIME"`, 1)), source.KindContractViolation, "", "avType mismatch"},
		{"rand が短い", 200, "application/json", vector(strings.Replace(good, strings.Repeat("01", 16), strings.Repeat("01", 15), 1)), source.KindContractViolation, "", "rand has 15 bytes"},
		{"xres が長い", 200, "application/json", vector(strings.Replace(good, strings.Repeat("03", 8), strings.Repeat("03", 17), 1)), source.KindContractViolation, "", "xres has 17 bytes"},
		{"ck が 16 進でない", 200, "application/json", vector(strings.Replace(good, strings.Repeat("04", 16), strings.Repeat("zz", 16), 1)), source.KindContractViolation, "", "ck is not hex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake, _ := newService(t)
			fake.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
				if tt.ctype != "" {
					w.Header().Set("Content-Type", tt.ctype)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
			se := wantKind(t, err, tt.wantKind)
			if se.Status != tt.status || se.Cause != tt.wantCause || !strings.HasPrefix(se.Reason, tt.wantReason) || se.SourceID != "vecgen" {
				t.Errorf("error = %+v", se)
			}
		})
	}

	t.Run("大文字の 16 進を受け付ける", func(t *testing.T) {
		svc, fake, _ := newService(t)
		upper := `{"avType":"EAP_AKA","rand":"` + strings.Repeat("AB", 16) + `","autn":"` + strings.Repeat("CD", 16) +
			`","xres":"` + strings.Repeat("EF", 8) + `","ck":"` + strings.Repeat("0A", 16) + `","ik":"` + strings.Repeat("0B", 16) + `","extra":1}`
		fake.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(vector(upper)))
		})
		v, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if !bytes.Equal(v.RAND, bytes.Repeat([]byte{0xAB}, 16)) {
			t.Errorf("RAND = %x", v.RAND)
		}
	})
}

func TestFetch_Timeout(t *testing.T) {
	svc, fake, reg := newService(t)
	fake.WriteRegistry(t, "200ms")
	if err := reg.Reload(nil); err != nil {
		t.Fatal(err)
	}
	fake.SetHandler(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	start := time.Now()
	_, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
	se := wantKind(t, err, source.KindTemporary)
	if se.Reason != "timeout" {
		t.Errorf("reason = %q, want timeout", se.Reason)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s, want about 200ms", d)
	}
}

func TestFetch_UntrustedServer(t *testing.T) {
	// 登録したサーバーの CA で確かめられないサーバーには、つながない（一時的な誤り）。
	fake := sourcetest.Start(t, "vecgen")
	path := fake.RegistryFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// サーバーの CA に、取得先とは別の証明書（クライアント証明書）を指す
	if err := os.WriteFile(path, bytes.Replace(data, []byte(`"server-ca.pem"`), []byte(`"client.pem"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := source.LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := source.NewService(fakeLookup{ids: map[string]string{testIMSI: "vecgen"}}, reg)
	_, err = svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
	wantKind(t, err, source.KindTemporary)
}

func TestFetch_CircuitBreaker(t *testing.T) {
	// 一時的な誤りが 5 回続くと遮断器が開き、取得先を呼ばずに KindCircuitOpen を返す。
	// 確定した拒否は数えない。
	svc, fake, reg := newService(t)
	fake.AllowOnly() // すべて 403
	for range 10 {
		_, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
		wantKind(t, err, source.KindRejected)
	}
	c, _ := reg.Get("vecgen")
	if c.State() != "closed" {
		t.Fatalf("breaker = %s after rejections, want closed", c.State())
	}

	fake.SetHandler(func(w http.ResponseWriter, _ *http.Request) { sourcetest.Problem(w, 500, "SYSTEM_FAILURE") })
	for range 5 {
		_, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
		wantKind(t, err, source.KindTemporary)
	}
	before := len(fake.Requests())
	_, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA})
	wantKind(t, err, source.KindCircuitOpen)
	if len(fake.Requests()) != before {
		t.Error("source was called while the breaker is open")
	}
	if c.State() != "open" {
		t.Errorf("breaker = %s, want open", c.State())
	}
	// 状態が変わったことを Changed で知らせ、Breakers に出す
	select {
	case <-reg.Changed():
	default:
		t.Error("Changed() did not fire")
	}
	if b := reg.Breakers()["vecgen"]; b.State != "open" || time.Since(b.Since) > 10*time.Second {
		t.Errorf("Breakers() = %+v", b)
	}

	// 読み直すと、遮断器は閉じた状態から始まる
	if err := reg.Reload(nil); err != nil {
		t.Fatal(err)
	}
	c, _ = reg.Get("vecgen")
	if c.State() != "closed" {
		t.Errorf("breaker after reload = %s, want closed", c.State())
	}
}

func TestProbe(t *testing.T) {
	_, fake, reg := newService(t)
	c, _ := reg.Get("vecgen")
	code, err := c.Probe(context.Background())
	if err != nil || code == 0 {
		t.Errorf("Probe() = %d %v", code, err)
	}
	fake.SetHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	if code, err := c.Probe(context.Background()); err != nil || code != http.StatusTeapot {
		t.Errorf("Probe() = %d %v", code, err)
	}
}

func TestLoadRegistry(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	reg, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	infos := reg.List()
	if len(infos) != 1 || infos[0].ID != "vecgen" || infos[0].Name != "fake vecgen" || infos[0].Timeout != time.Second ||
		infos[0].BaseURL != fake.BaseURL || infos[0].NotAfter.IsZero() {
		t.Errorf("List() = %+v", infos)
	}
}

func TestLoadRegistry_Errors(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	base := map[string]string{
		"id": `"vecgen"`, "baseUrl": `"` + fake.BaseURL + `"`, "clientCertFile": `"client.pem"`,
		"clientKeyFile": `"client.key"`, "serverCaFile": `"server-ca.pem"`,
	}
	entry := func(over map[string]string) string {
		var parts []string
		for _, k := range []string{"id", "baseUrl", "clientCertFile", "clientKeyFile", "serverCaFile", "timeout", "extra"} {
			v, ok := over[k]
			if !ok {
				v, ok = base[k]
			}
			if ok && v != "" {
				parts = append(parts, `"`+k+`":`+v)
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	tests := []struct {
		name string
		file string
		want string
	}{
		{"JSON でない", `{`, "parse sources file"},
		{"知らない項目", `{"sources":[` + entry(map[string]string{"extra": `1`}) + `]}`, "unknown field"},
		{"ID の形", `{"sources":[` + entry(map[string]string{"id": `"Vec_Gen"`}) + `]}`, "id must be"},
		{"平文の HTTP", `{"sources":[` + entry(map[string]string{"baseUrl": `"http://127.0.0.1:1/nudm-ueau/v1"`}) + `]}`, "baseUrl must be https"},
		{"パスの終わり", `{"sources":[` + entry(map[string]string{"baseUrl": `"https://127.0.0.1:1/api/v1"`}) + `]}`, "must end with /nudm-ueau/v1"},
		{"タイムアウトの範囲", `{"sources":[` + entry(map[string]string{"timeout": `"1m"`}) + `]}`, "timeout must be"},
		{"証明書がない", `{"sources":[` + entry(map[string]string{"clientCertFile": `"none.pem"`}) + `]}`, "load client certificate"},
		{"CA がない", `{"sources":[` + entry(map[string]string{"serverCaFile": `"none.pem"`}) + `]}`, "read server CA"},
		{"CA が証明書でない", `{"sources":[` + entry(map[string]string{"serverCaFile": `"client.key"`}) + `]}`, "server CA has no certificate"},
		{"ID の重複", `{"sources":[` + entry(nil) + `,` + entry(nil) + `]}`, "duplicate id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(fake.Dir, "bad.json")
			if err := os.WriteFile(path, []byte(tt.file), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := source.LoadRegistry(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("LoadRegistry error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Run("空の登録簿", func(t *testing.T) {
		path := filepath.Join(fake.Dir, "empty.json")
		if err := os.WriteFile(path, []byte(`{"sources":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		reg, err := source.LoadRegistry(path)
		if err != nil || len(reg.List()) != 0 {
			t.Errorf("LoadRegistry(empty) = %v, %v", reg, err)
		}
	})
}

func TestRegistry_ReloadKeepsOldOnError(t *testing.T) {
	// 読み直しに失敗したら、前の登録簿のまま続ける。
	svc, fake, reg := newService(t)
	if err := os.WriteFile(fake.RegistryFile, []byte(`{"sources":[{"id":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(nil); err == nil {
		t.Fatal("Reload returned nil for a bad file")
	}
	if _, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA}); err != nil {
		t.Errorf("Fetch after a failed reload: %v", err)
	}
}

func TestRegistry_ReloadRefusesRemovingUsedSource(t *testing.T) {
	// 消える取得先を確かめる関数が誤りを返せば、前の登録簿のまま続ける（MVP の設計概要 §7.1）。
	svc, fake, reg := newService(t)
	if err := os.WriteFile(fake.RegistryFile, []byte(`{"sources":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	err := reg.Reload(func(removed []string) error {
		got = removed
		return errors.New("in use")
	})
	if err == nil || len(got) != 1 || got[0] != "vecgen" {
		t.Fatalf("Reload err=%v removed=%v", err, got)
	}
	if _, err := svc.Fetch(context.Background(), &source.Request{IMSI: testIMSI, Method: source.MethodAKA}); err != nil {
		t.Errorf("Fetch after a refused reload: %v", err)
	}
	// 消してよければ読み直す
	if err := reg.Reload(func([]string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 0 {
		t.Errorf("List = %v", reg.List())
	}
}

func TestReadInfos(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	// 鍵のファイルがなくても読める（admin は鍵を読まない）
	if err := os.Remove(filepath.Join(fake.Dir, "client.key")); err != nil {
		t.Fatal(err)
	}
	infos, err := source.ReadInfos(fake.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "vecgen" || infos[0].Name != "fake vecgen" || infos[0].NotAfter.IsZero() || infos[0].Timeout != time.Second {
		t.Fatalf("infos = %+v", infos)
	}
	if len(infos[0].ClientCertFingerprint) != 64 || infos[0].ClientCertSubject == "" || len(infos[0].ServerCAs) != 1 || infos[0].ServerCAs[0].NotAfter.IsZero() {
		t.Errorf("certificate infos = %+v", infos[0])
	}
	for name, body := range map[string]string{
		"unknown field": `{"sources":[],"x":1}`,
		"bad id":        `{"sources":[{"id":"BAD","baseUrl":"https://h/nudm-ueau/v1","clientCertFile":"a","clientKeyFile":"b","serverCaFile":"c"}]}`,
		"duplicate":     `{"sources":[{"id":"a","baseUrl":"https://h/nudm-ueau/v1","clientCertFile":"a","clientKeyFile":"b","serverCaFile":"c"},{"id":"a","baseUrl":"https://h/nudm-ueau/v1","clientCertFile":"a","clientKeyFile":"b","serverCaFile":"c"}]}`,
	} {
		if err := os.WriteFile(fake.RegistryFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := source.ReadInfos(fake.RegistryFile); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestError(t *testing.T) {
	err := &source.Error{Kind: source.KindRejected, SourceID: "vecgen", Status: 403, Cause: "AUTHENTICATION_REJECTED", Reason: "r", Err: errors.New("e")}
	if got := err.Error(); got != `source "vecgen": rejected (status 403, cause AUTHENTICATION_REJECTED): r: e` {
		t.Errorf("Error() = %q", got)
	}
	if source.KindOf(fmtWrap(err)) != source.KindRejected || source.KindOf(errors.New("x")) != 0 {
		t.Error("KindOf does not see through wrapping")
	}
	for k, want := range map[source.Kind]string{
		source.KindRejected: "rejected", source.KindTemporary: "temporary", source.KindContractViolation: "contract_violation",
		source.KindCircuitOpen: "circuit_open", source.KindSourceUnknown: "source_unknown", source.KindLookupFailed: "lookup_failed", 0: "unknown",
	} {
		if k.String() != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, k.String(), want)
		}
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("outer"), err) }
