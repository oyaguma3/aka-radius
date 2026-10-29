package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/auth/eap"
	"github.com/oyaguma3/aka-radius/internal/auth/eap/aka"
	"github.com/oyaguma3/aka-radius/internal/auth/session"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/envconf"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	eapaka "github.com/oyaguma3/go-eapaka"
)

// 試験の値
const (
	testTraceID     = "test-trace-id-123"
	testIMSI        = "001010123456789"
	testRealm       = "@wlan.mnc001.mcc001.3gppnetwork.org"
	testNASID       = "edge-01"
	testCalled      = "aa-bb-cc-dd-ee-ff:TestSSID"
	testSrcIP       = "192.0.2.1"
	testNetworkName = "WLAN"
	testSourceID    = "vecgen"
	testTenant      = 42
)

var (
	testRAND = bytes.Repeat([]byte{0x01}, 16)
	testAUTN = bytes.Repeat([]byte{0x10}, 16)
	testXRES = bytes.Repeat([]byte{0x20}, 8)
	testCK   = bytes.Repeat([]byte{0x30}, 16)
	testIK   = bytes.Repeat([]byte{0x40}, 16)
)

// ---- 偽物 ----

// fakeCtxStore はメモリーの EAP の会話の状態。
type fakeCtxStore struct {
	mu        sync.Mutex
	m         map[string]map[string]any
	createErr error
	updateErr error
}

func (f *fakeCtxStore) Create(_ context.Context, id string, c *session.EAPContext) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id] = map[string]any{
		"imsi": c.IMSI, "stage": c.Stage, "eap_type": c.EAPType, "identity": c.Identity, "source_id": c.SourceID,
		"tenant_id": c.TenantID, "rule_index": c.RuleIndex, "vlan_id": c.VlanID, "session_timeout": c.SessionTimeout,
		"permanent_id_requested": c.PermanentIDRequested,
	}
	return nil
}

func (f *fakeCtxStore) Get(_ context.Context, id string) (*session.EAPContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.m[id]
	if !ok {
		return nil, session.ErrContextNotFound
	}
	c := &session.EAPContext{}
	c.IMSI, _ = m["imsi"].(string)
	c.Stage, _ = m["stage"].(string)
	c.EAPType, _ = m["eap_type"].(uint8)
	c.Identity, _ = m["identity"].(string)
	c.SourceID, _ = m["source_id"].(string)
	c.TenantID, _ = m["tenant_id"].(int64)
	c.RuleIndex, _ = m["rule_index"].(int)
	c.VlanID, _ = m["vlan_id"].(int)
	c.SessionTimeout, _ = m["session_timeout"].(int)
	c.ResyncCount, _ = m["resync_count"].(int)
	c.RAND, _ = m["rand"].(string)
	c.XRES, _ = m["xres"].(string)
	c.Kaut, _ = m["k_aut"].(string)
	c.MSK, _ = m["msk"].(string)
	return c, nil
}

func (f *fakeCtxStore) Update(_ context.Context, id string, u map[string]any) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.m[id]
	if !ok {
		return session.ErrContextNotFound
	}
	maps.Copy(m, u)
	return nil
}

func (f *fakeCtxStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

func (f *fakeCtxStore) Exists(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[id]
	return ok, nil
}

func (f *fakeCtxStore) set(id, key string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id][key] = v
}

// fakeSessStore はメモリーのセッション。
type fakeSessStore struct {
	sessions  map[string]sessions.Session
	createErr error
}

func (f *fakeSessStore) Create(_ context.Context, s sessions.Session, _ time.Time) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.sessions[s.ID] = s
	return nil
}

// fakeUsage は利用の集計。
type fakeUsage struct {
	authOK map[string]int
	err    error
}

func (f *fakeUsage) AddAuthOK(_ context.Context, _ time.Time, tenantID int64, imsi string) error {
	if f.err != nil {
		return f.err
	}
	f.authOK[fmt.Sprintf("%d:%s", tenantID, imsi)]++
	return nil
}

// fakeSubs はメモリーの加入者。
type fakeSubs struct {
	m   map[string]subscriber.Subscriber
	err error
}

func (f *fakeSubs) Get(_ context.Context, imsi string) (subscriber.Subscriber, error) {
	if f.err != nil {
		return subscriber.Subscriber{}, f.err
	}
	s, ok := f.m[imsi]
	if !ok {
		return subscriber.Subscriber{}, subscriber.ErrNotFound
	}
	return s, nil
}

// fakeTrust は刻印の信頼の印。
type fakeTrust struct {
	trusted bool
	err     error
}

func (f fakeTrust) TrustsStamp(context.Context, string) (bool, error) { return f.trusted, f.err }

// fakeFetcher は取得先。要求を記録する。
type fakeFetcher struct {
	reqs []source.Request
	err  error
}

func (f *fakeFetcher) Fetch(_ context.Context, req *source.Request) (*source.Vector, error) {
	f.reqs = append(f.reqs, *req)
	if f.err != nil {
		return nil, f.err
	}
	return &source.Vector{RAND: testRAND, AUTN: testAUTN, XRES: testXRES, CK: testCK, IK: testIK, SourceID: req.SourceID}, nil
}

// testEnv は試験のエンジンと偽物。
type testEnv struct {
	eng     *EngineImpl
	ctx     *fakeCtxStore
	sess    *fakeSessStore
	usage   *fakeUsage
	subs    *fakeSubs
	fetcher *fakeFetcher
	trust   *fakeTrust
	stamps  [][]byte
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{
		ctx:   &fakeCtxStore{m: map[string]map[string]any{}},
		sess:  &fakeSessStore{sessions: map[string]sessions.Session{}},
		usage: &fakeUsage{authOK: map[string]int{}},
		subs: &fakeSubs{m: map[string]subscriber.Subscriber{testIMSI: {
			IMSI: testIMSI, State: subscriber.StateActive, SourceID: testSourceID,
			Rules: []subscriber.Rule{
				{TenantID: 7, NasID: "*", SSIDs: []string{"*"}},
				{TenantID: testTenant, NasID: testNASID, SSIDs: []string{"testssid"}, VlanID: 100, SessionTimeout: 3600},
			},
		}}},
		fetcher: &fakeFetcher{},
		trust:   &fakeTrust{trusted: true},
		stamps:  [][]byte{[]byte("42")},
	}
	cfg := &config.Config{NetworkName: testNetworkName, Common: envconf.Common{LogMaskIMSI: false}}
	env.eng = NewEngine(env.fetcher, env.ctx, env.sess, env.usage, env.subs, env.trust, cfg)
	return env
}

// request は Access-Request の EAP の部分を作る。state が空なら最初の要求。
func (env *testEnv) request(userName string, msg []byte, state string) *eap.Request {
	r := &eap.Request{
		TraceID: testTraceID, SrcIP: testSrcIP, NASIdentifier: testNASID, CalledStation: testCalled,
		UserName: userName, EAPMessage: msg, Stamps: env.stamps,
	}
	if state != "" {
		r.State = []byte(state)
	}
	return r
}

// captureLogs は試験の間の slog の出力を集める。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func wantLog(t *testing.T, logs *bytes.Buffer, eventID string) {
	t.Helper()
	if !strings.Contains(logs.String(), `"event_id":"`+eventID+`"`) {
		t.Errorf("log does not contain %s: %s", eventID, logs.String())
	}
}

// ---- EAP のメッセージ ----

func identityMsg(id uint8, identity string) []byte {
	b := make([]byte, 5+len(identity))
	b[0], b[1], b[4] = 2, id, 1
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	copy(b[5:], identity)
	return b
}

func akaMsg(id uint8, typ, subtype uint8, attrs ...eapaka.Attribute) []byte {
	p := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: id, Type: typ, Subtype: subtype, Attributes: attrs}
	data, _ := p.Marshal()
	return data
}

func challengeResp(t *testing.T, id, typ uint8, kAut, res []byte) []byte {
	t.Helper()
	attrs := []eapaka.Attribute{&eapaka.AtRes{Res: res}}
	if typ == eapaka.TypeAKAPrime {
		attrs = append(attrs, &eapaka.AtKdf{KDF: eapaka.KDFAKAPrimeWithCKIK})
	}
	attrs = append(attrs, &eapaka.AtMac{MAC: make([]byte, 16)})
	p := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: id, Type: typ, Subtype: eapaka.SubtypeChallenge, Attributes: attrs}
	if err := p.CalculateAndSetMac(kAut); err != nil {
		t.Fatal(err)
	}
	data, _ := p.Marshal()
	return data
}

func eapCode(res *eap.Result) byte {
	if len(res.EAPMessage) == 0 {
		return 0
	}
	return res.EAPMessage[0]
}

func wantReject(t *testing.T, res *eap.Result) {
	t.Helper()
	if res.Action != eap.ActionReject || eapCode(res) != 4 {
		t.Fatalf("result = %+v, want Reject with EAP-Failure", res)
	}
}

// startChallenge は最初の要求から Challenge まで進め、端末の K_aut を返す。
func (env *testEnv) startChallenge(t *testing.T, identity string) (*eap.Result, []byte) {
	t.Helper()
	res := env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), ""))
	if res.Action != eap.ActionChallenge {
		t.Fatalf("result = %+v, want Challenge", res)
	}
	return res, aka.DeriveKeys(identity, testCK, testIK).K_aut
}

// ---- 試験 ----

func TestEngine_FullAuth(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		typ      uint8
		wantMeth source.Method
	}{
		{"EAP-AKA", "0", eapaka.TypeAKA, source.MethodAKA},
		{"EAP-AKA'", "6", eapaka.TypeAKAPrime, source.MethodAKAPrime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t)
			identity := tt.prefix + testIMSI + testRealm
			res := env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), ""))
			if res.Action != eap.ActionChallenge || string(res.State) != testTraceID {
				t.Fatalf("result = %+v", res)
			}
			// 取得先は判定で読んだ加入者の取得先。方式・anId・トレースID を渡す
			want := source.Request{IMSI: testIMSI, SourceID: testSourceID, Method: tt.wantMeth, ANID: testNetworkName, TraceID: testTraceID}
			if len(env.fetcher.reqs) != 1 || env.fetcher.reqs[0] != want {
				t.Fatalf("fetch = %+v, want %+v", env.fetcher.reqs, want)
			}
			c, _ := env.ctx.Get(context.Background(), testTraceID)
			if c.Identity != identity || c.TenantID != testTenant || c.RuleIndex != 1 || c.VlanID != 100 || c.SessionTimeout != 3600 {
				t.Errorf("context = %+v", c)
			}

			kAut, _ := hex.DecodeString(c.Kaut)
			res = env.eng.Process(context.Background(), env.request(identity, challengeResp(t, 2, tt.typ, kAut, testXRES), testTraceID))
			if res.Action != eap.ActionAccept || eapCode(res) != 3 {
				t.Fatalf("result = %+v, want Accept", res)
			}
			if res.VlanID != "100" || res.SessionTimeout != 3600 || len(res.MSK) != 64 {
				t.Errorf("accept = %+v", res)
			}
			if s, ok := env.sess.sessions[res.SessionID]; !ok || s.TenantID != testTenant || s.IMSI != testIMSI || s.NasIP != testSrcIP || s.NasIdentifier != testNASID {
				t.Errorf("session = %+v", s)
			}
			// 利用の集計に認証の成功を 1 足す（段階 2-4 の J7）
			if n := env.usage.authOK[fmt.Sprintf("%d:%s", testTenant, testIMSI)]; n != 1 {
				t.Errorf("auth_ok = %d", n)
			}
			if _, err := env.ctx.Get(context.Background(), testTraceID); err == nil {
				t.Error("context remains after Accept")
			}
		})
	}
}

func TestEngine_RejectBeforeVector(t *testing.T) {
	// ベクターを取る前に、加入者の有無・状態・刻印・ルールを確かめる。拒否なら取得先を呼ばない（MVP の設計概要 §8.1）
	tests := []struct {
		name  string
		setup func(*testEnv)
		event string
	}{
		{"未登録", func(e *testEnv) { delete(e.subs.m, testIMSI) }, "AUTH_SUBSCRIBER_NOT_FOUND"},
		{"形の違う加入者", func(e *testEnv) { e.subs.err = subscriber.ErrInvalid }, "AUTH_SUBSCRIBER_INVALID"},
		{"加入者を読めない", func(e *testEnv) { e.subs.err = errors.New("valkey down") }, "AUTH_SUBSCRIBER_LOOKUP_ERR"},
		{"inactive", func(e *testEnv) { e.setState(subscriber.StateInactive) }, "AUTH_SUBSCRIBER_INACTIVE"},
		{"terminated", func(e *testEnv) { e.setState(subscriber.StateTerminated) }, "AUTH_SUBSCRIBER_TERMINATED"},
		{"刻印がない", func(e *testEnv) { e.stamps = nil }, "AUTH_STAMP_MISSING"},
		{"刻印を信頼しない送信元", func(e *testEnv) { e.trust.trusted = false }, "AUTH_STAMP_MISSING"},
		{"信頼の印を読めない", func(e *testEnv) { e.trust.err = errors.New("valkey down") }, "AUTH_STAMP_LOOKUP_ERR"},
		{"刻印が 2 つ", func(e *testEnv) { e.stamps = [][]byte{[]byte("42"), []byte("42")} }, "AUTH_STAMP_MULTIPLE"},
		{"刻印が数でない", func(e *testEnv) { e.stamps = [][]byte{[]byte("abc")} }, "AUTH_STAMP_INVALID"},
		{"刻印に符号", func(e *testEnv) { e.stamps = [][]byte{[]byte("+42")} }, "AUTH_STAMP_INVALID"},
		{"刻印が 0", func(e *testEnv) { e.stamps = [][]byte{[]byte("0")} }, "AUTH_STAMP_INVALID"},
		{"ルールのテナントが違う", func(e *testEnv) { e.stamps = [][]byte{[]byte("43")} }, "AUTH_POLICY_DENIED"},
		{"ルールの NAS-Identifier が違う", func(e *testEnv) {
			e.setRules(subscriber.Rule{TenantID: testTenant, NasID: "edge-02", SSIDs: []string{"*"}})
		}, "AUTH_POLICY_DENIED"},
		{"ルールの SSID が違う", func(e *testEnv) {
			e.setRules(subscriber.Rule{TenantID: testTenant, NasID: "*", SSIDs: []string{"other"}})
		}, "AUTH_POLICY_DENIED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			tt.setup(env)
			identity := "0" + testIMSI + testRealm
			wantReject(t, env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), "")))
			if len(env.fetcher.reqs) != 0 {
				t.Errorf("source was called %d times", len(env.fetcher.reqs))
			}
			wantLog(t, logs, tt.event)
		})
	}
}

func (env *testEnv) setState(st subscriber.State) {
	s := env.subs.m[testIMSI]
	s.State = st
	env.subs.m[testIMSI] = s
}

func (env *testEnv) setRules(rules ...subscriber.Rule) {
	s := env.subs.m[testIMSI]
	s.Rules = rules
	env.subs.m[testIMSI] = s
}

func TestEngine_InitialIdentityErrors(t *testing.T) {
	permanent := "0" + testIMSI + testRealm
	tests := []struct {
		name  string
		msg   []byte
		drop  bool
		event string
	}{
		{"Identity の長さが壊れている", []byte{2, 1, 0, 99, 1, 'a'}, false, "EAP_IDENTITY_INVALID"},
		{"EAP-SIM の Identity", identityMsg(1, "1"+testIMSI+testRealm), false, "EAP_UNSUPPORTED_TYPE"},
		{"realm のない Identity", identityMsg(1, "0"+testIMSI), false, "EAP_IDENTITY_INVALID"},
		{"AKA-Identity に AT_IDENTITY がない", akaMsg(1, eapaka.TypeAKA, eapaka.SubtypeIdentity), false, "EAP_IDENTITY_INVALID"},
		{"最初が Challenge", akaMsg(1, eapaka.TypeAKA, eapaka.SubtypeChallenge), true, "EAP_NOT_IDENTITY"},
		{"EAP として読めない", []byte{2, 1, 0, 8, 23, 5}, false, "EAP_PARSE_ERR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			res := env.eng.Process(context.Background(), env.request(permanent, tt.msg, ""))
			if tt.drop {
				if res.Action != eap.ActionDrop {
					t.Fatalf("result = %+v, want Drop", res)
				}
			} else {
				wantReject(t, res)
			}
			if len(env.fetcher.reqs) != 0 {
				t.Error("source was called")
			}
			wantLog(t, logs, tt.event)
		})
	}

	t.Run("最初の AKA-Identity の AT_IDENTITY", func(t *testing.T) {
		env := newEnv(t)
		res := env.eng.Process(context.Background(), env.request("", akaMsg(1, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: permanent}), ""))
		if res.Action != eap.ActionChallenge {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestEngine_IdentityFromEAPNotUserName(t *testing.T) {
	// IMSI と鍵の導出は EAP の Identity から取る。User-Name が違えばログに出す（MVP の設計概要 §8.1。決定事項 8）
	logs := captureLogs(t)
	env := newEnv(t)
	identity := "0" + testIMSI + testRealm
	res := env.eng.Process(context.Background(), env.request("anonymous"+testRealm, identityMsg(1, identity), ""))
	if res.Action != eap.ActionChallenge {
		t.Fatalf("result = %+v", res)
	}
	wantLog(t, logs, "EAP_IDENTITY_MISMATCH")
	kAut := aka.DeriveKeys(identity, testCK, testIK).K_aut
	res = env.eng.Process(context.Background(), env.request("anonymous"+testRealm, challengeResp(t, 2, eapaka.TypeAKA, kAut, testXRES), testTraceID))
	if res.Action != eap.ActionAccept {
		t.Fatalf("result = %+v, want Accept (keys derived from the EAP identity)", res)
	}
}

func TestEngine_PseudonymThenPermanent(t *testing.T) {
	pseudonym := "2pseudo" + testRealm
	permanent := "0" + testIMSI + testRealm
	start := func(t *testing.T, env *testEnv) {
		t.Helper()
		res := env.eng.Process(context.Background(), env.request(pseudonym, identityMsg(1, pseudonym), ""))
		if res.Action != eap.ActionChallenge || res.EAPMessage[0] != 1 {
			t.Fatalf("result = %+v, want AKA-Identity request", res)
		}
		if len(env.fetcher.reqs) != 0 {
			t.Fatal("source was called for a pseudonym")
		}
	}

	t.Run("AT_IDENTITY の永続ID で通る", func(t *testing.T) {
		env := newEnv(t)
		start(t, env)
		res := env.eng.Process(context.Background(), env.request(pseudonym,
			akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: permanent}), testTraceID))
		if res.Action != eap.ActionChallenge || len(env.fetcher.reqs) != 1 {
			t.Fatalf("result = %+v", res)
		}
		c, _ := env.ctx.Get(context.Background(), testTraceID)
		if c.Identity != permanent || c.TenantID != testTenant {
			t.Errorf("context = %+v", c)
		}
		kAut := aka.DeriveKeys(permanent, testCK, testIK).K_aut
		res = env.eng.Process(context.Background(), env.request(pseudonym, challengeResp(t, 3, eapaka.TypeAKA, kAut, testXRES), testTraceID))
		if res.Action != eap.ActionAccept {
			t.Fatalf("result = %+v", res)
		}
	})

	tests := []struct {
		name  string
		setup func(*testEnv)
		msg   []byte
		event string
	}{
		{"AT_IDENTITY がない", nil, akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity), "EAP_IDENTITY_INVALID"},
		{"AT_IDENTITY が仮名", nil, akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: pseudonym}), "EAP_IDENTITY_INVALID"},
		{"AT_IDENTITY が EAP-SIM", nil, akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: "1" + testIMSI + testRealm}), "EAP_UNSUPPORTED_TYPE"},
		{"加入者が inactive", func(e *testEnv) { e.setState(subscriber.StateInactive) },
			akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: permanent}), "AUTH_SUBSCRIBER_INACTIVE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			start(t, env)
			if tt.setup != nil {
				tt.setup(env)
			}
			wantReject(t, env.eng.Process(context.Background(), env.request(pseudonym, tt.msg, testTraceID)))
			if len(env.fetcher.reqs) != 0 {
				t.Error("source was called")
			}
			if _, err := env.ctx.Get(context.Background(), testTraceID); err == nil {
				t.Error("context remains")
			}
			wantLog(t, logs, tt.event)
		})
	}

	t.Run("Challenge の後の AKA-Identity は拒否", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		env.startChallenge(t, permanent)
		wantReject(t, env.eng.Process(context.Background(), env.request(permanent,
			akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeIdentity, &eapaka.AtIdentity{Identity: permanent}), testTraceID)))
		wantLog(t, logs, "EAP_UNEXPECTED_IDENTITY")
	})
}

func TestEngine_ChallengeFailures(t *testing.T) {
	identity := "0" + testIMSI + testRealm
	tests := []struct {
		name  string
		setup func(*testEnv)
		msg   func(t *testing.T, kAut []byte) []byte
		event string
	}{
		{"AT_MAC が違う", nil, func(t *testing.T, _ []byte) []byte {
			return challengeResp(t, 2, eapaka.TypeAKA, bytes.Repeat([]byte{9}, 16), testXRES)
		}, "AUTH_MAC_INVALID"},
		{"RES が違う", nil, func(t *testing.T, k []byte) []byte {
			return challengeResp(t, 2, eapaka.TypeAKA, k, bytes.Repeat([]byte{0xff}, 8))
		}, "AUTH_RES_MISMATCH"},
		{"Accept の前に inactive", func(e *testEnv) { e.setState(subscriber.StateInactive) }, nil, "AUTH_SUBSCRIBER_INACTIVE"},
		{"Accept の前に削除", func(e *testEnv) { delete(e.subs.m, testIMSI) }, nil, "AUTH_SUBSCRIBER_NOT_FOUND"},
		{"セッションを作れない", func(e *testEnv) { e.sess.createErr = errors.New("valkey down") }, nil, "SESSION_CREATE_ERR"},
		{"会話の状態の値が壊れている", func(e *testEnv) { e.ctx.set(testTraceID, "xres", "zz") }, nil, "EAP_CTX_DECODE_ERR"},
		{"Challenge を送る前", func(e *testEnv) { e.ctx.set(testTraceID, "stage", string(eap.StateWaitingIdentity)) }, nil, "EAP_STATE_ERR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			_, kAut := env.startChallenge(t, identity)
			if tt.setup != nil {
				tt.setup(env)
			}
			msg := challengeResp(t, 2, eapaka.TypeAKA, kAut, testXRES)
			if tt.msg != nil {
				msg = tt.msg(t, kAut)
			}
			wantReject(t, env.eng.Process(context.Background(), env.request(identity, msg, testTraceID)))
			wantLog(t, logs, tt.event)
		})
	}

	t.Run("利用の集計に書けなくても Accept", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		_, kAut := env.startChallenge(t, identity)
		env.usage.err = errors.New("valkey down")
		res := env.eng.Process(context.Background(), env.request(identity, challengeResp(t, 2, eapaka.TypeAKA, kAut, testXRES), testTraceID))
		if res.Action != eap.ActionAccept {
			t.Fatalf("result = %+v", res)
		}
		wantLog(t, logs, "USAGE_WRITE_ERR")
	})

	t.Run("VLAN のないルール", func(t *testing.T) {
		env := newEnv(t)
		env.setRules(subscriber.Rule{TenantID: testTenant, NasID: "*", SSIDs: []string{"*"}})
		_, kAut := env.startChallenge(t, identity)
		res := env.eng.Process(context.Background(), env.request(identity, challengeResp(t, 2, eapaka.TypeAKA, kAut, testXRES), testTraceID))
		if res.Action != eap.ActionAccept || res.VlanID != "" || res.SessionTimeout != 0 {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestEngine_Resync(t *testing.T) {
	// 再同期の鍵の導出には、判定のときに保存した Identity を使う（User-Name は見ない）
	identity := "0" + testIMSI + testRealm
	auts := bytes.Repeat([]byte{0x5a}, 14)
	syncFail := func(id uint8) []byte {
		return akaMsg(id, eapaka.TypeAKA, eapaka.SubtypeSynchronizationFailure, &eapaka.AtAuts{Auts: auts})
	}

	t.Run("1 回目は取り直して通る", func(t *testing.T) {
		env := newEnv(t)
		_, kAut := env.startChallenge(t, identity)
		res := env.eng.Process(context.Background(), env.request("other"+testRealm, syncFail(2), testTraceID))
		if res.Action != eap.ActionChallenge {
			t.Fatalf("result = %+v", res)
		}
		if len(env.fetcher.reqs) != 2 {
			t.Fatalf("fetch = %d", len(env.fetcher.reqs))
		}
		r := env.fetcher.reqs[1]
		if r.SourceID != testSourceID || r.Resync == nil || !bytes.Equal(r.Resync.RAND, testRAND) || !bytes.Equal(r.Resync.AUTS, auts) {
			t.Errorf("resync request = %+v", r)
		}
		res = env.eng.Process(context.Background(), env.request("other"+testRealm, challengeResp(t, 3, eapaka.TypeAKA, kAut, testXRES), testTraceID))
		if res.Action != eap.ActionAccept {
			t.Fatalf("result = %+v", res)
		}
	})

	tests := []struct {
		name  string
		setup func(*testEnv)
		msg   []byte
		event string
	}{
		{"2 回目は拒否", func(e *testEnv) { e.ctx.set(testTraceID, "resync_count", 1) }, syncFail(2), "AUTH_RESYNC_LIMIT"},
		{"AT_AUTS がない", nil, akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeSynchronizationFailure), "EAP_PARSE_ERR"},
		{"RAND が壊れている", func(e *testEnv) { e.ctx.set(testTraceID, "rand", "00") }, syncFail(2), "EAP_CTX_INVALID"},
		{"取り直しの誤り", func(e *testEnv) {
			e.fetcher.err = &source.Error{Kind: source.KindTemporary, SourceID: testSourceID}
		}, syncFail(2), "VECTOR_TEMPORARY_ERR"},
		{"会話の状態を更新できない", func(e *testEnv) { e.ctx.updateErr = errors.New("valkey down") }, syncFail(2), "EAP_CTX_UPDATE_ERR"},
		{"Challenge を送る前", func(e *testEnv) { e.ctx.set(testTraceID, "stage", string(eap.StateIdentityReceived)) }, syncFail(2), "EAP_STATE_ERR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			env.startChallenge(t, identity)
			if tt.setup != nil {
				tt.setup(env)
			}
			wantReject(t, env.eng.Process(context.Background(), env.request(identity, tt.msg, testTraceID)))
			wantLog(t, logs, tt.event)
		})
	}
}

func TestEngine_PeerEndsConversation(t *testing.T) {
	identity := "0" + testIMSI + testRealm
	tests := []struct {
		name  string
		msg   []byte
		event string
	}{
		{"Authentication-Reject", akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeAuthenticationReject), "EAP_AUTH_REJECT"},
		{"Client-Error", akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeClientError, &eapaka.AtClientErrorCode{Code: 0}), "EAP_CLIENT_ERROR"},
		{"知らない Subtype", akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeNotification), "EAP_UNKNOWN_SUBTYPE"},
		{"EAP として読めない", []byte{2, 2, 0, 8, 23, 1}, "EAP_PARSE_ERR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			env.startChallenge(t, identity)
			wantReject(t, env.eng.Process(context.Background(), env.request(identity, tt.msg, testTraceID)))
			wantLog(t, logs, tt.event)
		})
	}

	t.Run("会話の状態がない", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		wantReject(t, env.eng.Process(context.Background(), env.request(identity, akaMsg(2, eapaka.TypeAKA, eapaka.SubtypeChallenge), "unknown")))
		wantLog(t, logs, "EAP_CTX_NOT_FOUND")
	})
}

func TestEngine_ContextStoreErrors(t *testing.T) {
	identity := "0" + testIMSI + testRealm
	t.Run("作れない", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		env.ctx.createErr = errors.New("valkey down")
		wantReject(t, env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), "")))
		if len(env.fetcher.reqs) != 0 {
			t.Error("source was called")
		}
		wantLog(t, logs, "EAP_CTX_CREATE_ERR")
	})
	t.Run("ベクターの後に更新できない", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		env.ctx.updateErr = errors.New("valkey down")
		wantReject(t, env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), "")))
		wantLog(t, logs, "EAP_CTX_UPDATE_ERR")
	})
	t.Run("仮名の会話を作れない", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		env.ctx.createErr = errors.New("valkey down")
		wantReject(t, env.eng.Process(context.Background(), env.request("", identityMsg(1, "2p"+testRealm), "")))
		wantLog(t, logs, "EAP_CTX_CREATE_ERR")
	})
	t.Run("仮名の会話を更新できない", func(t *testing.T) {
		logs := captureLogs(t)
		env := newEnv(t)
		env.ctx.updateErr = errors.New("valkey down")
		wantReject(t, env.eng.Process(context.Background(), env.request("", identityMsg(1, "2p"+testRealm), "")))
		wantLog(t, logs, "EAP_CTX_UPDATE_ERR")
	})
}

func TestEngine_VectorErrorLogs(t *testing.T) {
	// 取得の誤りは分類ごとの event_id で、取得先の ID を付けて記録し、EAP-Failure で拒否する。
	tests := []struct {
		name      string
		err       error
		wantEvent string
		wantLevel string
	}{
		{"一般的な拒否", &source.Error{Kind: source.KindRejected, SourceID: testSourceID, Status: 403, Cause: "AUTHENTICATION_REJECTED"}, "VECTOR_REJECTED", "WARN"},
		{"設定の食い違い", &source.Error{Kind: source.KindRejected, SourceID: testSourceID, Status: 400, Cause: "OPTIONAL_IE_INCORRECT"}, "VECTOR_REJECTED", "ERROR"},
		{"一時的な誤り", &source.Error{Kind: source.KindTemporary, SourceID: testSourceID, Reason: "timeout", LatencyMs: 3000, Err: errors.New("deadline")}, "VECTOR_TEMPORARY_ERR", "ERROR"},
		{"契約違反", &source.Error{Kind: source.KindContractViolation, SourceID: testSourceID, Status: 200, Reason: "avType mismatch"}, "VECTOR_CONTRACT_VIOLATION", "ERROR"},
		{"遮断器", &source.Error{Kind: source.KindCircuitOpen, SourceID: testSourceID}, "VECTOR_CIRCUIT_OPEN", "ERROR"},
		{"取得先が決まらない", &source.Error{Kind: source.KindSourceUnknown, Reason: "source not in registry"}, "VECTOR_SOURCE_UNKNOWN", "WARN"},
		{"加入者を読めない", &source.Error{Kind: source.KindLookupFailed}, "VECTOR_SOURCE_LOOKUP_ERR", "ERROR"},
		{"分類のない誤り", &source.Error{Kind: 99}, "VECTOR_UNKNOWN_ERR", "ERROR"},
		{"source.Error でない誤り", errors.New("boom"), "VECTOR_UNKNOWN_ERR", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			env := newEnv(t)
			env.fetcher.err = tt.err
			identity := "0" + testIMSI + testRealm
			wantReject(t, env.eng.Process(context.Background(), env.request(identity, identityMsg(1, identity), "")))
			out := logs.String()
			for _, want := range []string{`"event_id":"` + tt.wantEvent + `"`, `"level":"` + tt.wantLevel + `"`, `"trace_id":"` + testTraceID + `"`} {
				if !strings.Contains(out, want) {
					t.Errorf("log does not contain %s: %s", want, out)
				}
			}
			// 拒否したら会話の状態を残さない
			if _, err := env.ctx.Get(context.Background(), testTraceID); err == nil {
				t.Error("context remains")
			}
		})
	}
}
