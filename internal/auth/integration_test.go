package auth

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/auth/source/sourcetest"
	"github.com/oyaguma3/aka-radius/internal/auth/store"
	"github.com/oyaguma3/aka-radius/internal/envconf"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	eapaka "github.com/oyaguma3/go-eapaka"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"
	"layeh.com/radius/vendors/microsoft"
)

// 結合試験: RADIUS のパケットから、auth の EAP の処理、取得先のクライアント（mTLS）、偽の取得先までを通す
// （MVP の設計概要 §15 の結合。本物の aka-vecgen との結合は検証機で行う）。

const (
	itSecret = "testing123"
	itIMSI   = "001010000000001"
	itRealm  = "@wlan.mnc001.mcc001.3gppnetwork.org"
)

// itEnv は結合試験の環境。
type itEnv struct {
	mr   *miniredis.Miniredis
	fake *sourcetest.Server
	addr string
	subs *subscriber.Store
	// lastReq は最後に送った Access-Request（MS-MPPE の鍵の復号に要る）。
	lastReq *radius.Packet
	// stamps は Access-Request に付けるテナントの刻印（既定は itTenant の 1 つ）。
	stamps []string
}

// itTenant は結合試験のテナントの ID。
const itTenant = 42

// startAuth は miniredis、偽の取得先、auth の RADIUS サーバーを立てる。
// RADIUS クライアント 127.0.0.1 は刻印を信頼する。加入者 itIMSI は active で偽の取得先を指し、
// テナント itTenant のどの NAS-Identifier・SSID も VLAN 100 で許す。
func startAuth(t *testing.T) *itEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.HSet("client:127.0.0.1", "secret", itSecret, "trust_stamp", "1")

	fake := sourcetest.Start(t, "vecgen")
	registry, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	host, port, _ := net.SplitHostPort(mr.Addr())
	cfg := &config.Config{
		Common:          envconf.Common{ValkeyHost: host, ValkeyPort: port, LogLevel: "INFO"},
		NetworkName:     "WLAN",
		StampVendorID:   config.DefaultStampVendorID,
		StampVendorType: config.DefaultStampVendorType,
		Guard:           radiussrv.GuardConfig{RequestTimeout: 4 * time.Second, MaxInFlight: 16},
	}
	vc, err := store.NewValkeyClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vc.Close() })
	subs := subscriber.NewStore(vc.Client())
	if err := subs.Create(context.Background(), subscriber.Subscriber{
		IMSI: itIMSI, State: subscriber.StateActive, SourceID: "vecgen",
		Rules: []subscriber.Rule{{TenantID: itTenant, NasID: "*", SSIDs: []string{"*"}, VlanID: 100, SessionTimeout: 3600}},
	}); err != nil {
		t.Fatal(err)
	}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddr = pc.LocalAddr().String()
	srv := newServer(cfg, vc, registry)
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &itEnv{mr: mr, fake: fake, addr: cfg.ListenAddr, subs: subs, stamps: []string{"42"}}
}

// vsa はテナントの刻印の Vendor-Specific を作る。
func vsa(value string) radius.Attribute {
	b := binary.BigEndian.AppendUint32(nil, config.DefaultStampVendorID)
	b = append(b, config.DefaultStampVendorType, byte(2+len(value)))
	return append(b, value...)
}

// exchange は EAP-Message（と State）を載せた Access-Request を送り、応答を返す。
func (e *itEnv) exchange(t *testing.T, userName string, eapMsg, state []byte) *radius.Packet {
	t.Helper()
	p := radius.New(radius.CodeAccessRequest, []byte(itSecret))
	_ = rfc2865.UserName_SetString(p, userName)
	_ = rfc2865.NASIdentifier_SetString(p, "edge-01")
	_ = rfc2865.CalledStationID_SetString(p, "00-11-22-33-44-55:TestSSID")
	_ = rfc2869.EAPMessage_Set(p, eapMsg)
	if state != nil {
		_ = rfc2865.State_Set(p, state)
	}
	for _, v := range e.stamps {
		p.Add(rfc2865.VendorSpecific_Type, vsa(v))
	}
	radiussrv.SetMessageAuthenticator(p, []byte(itSecret), p.Authenticator)
	e.lastReq = p
	return e.send(t, p)
}

// send は Access-Request を送り、応答を返す。
func (e *itEnv) send(t *testing.T, p *radius.Packet) *radius.Packet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := radius.Exchange(ctx, p, e.addr)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return resp
}

// stampsOnly は exchange と同じ Access-Request を作って lastReq に置く（送らない）。
func (e *itEnv) stampsOnly(t *testing.T, userName string, eapMsg, state []byte) {
	t.Helper()
	p := radius.New(radius.CodeAccessRequest, []byte(itSecret))
	_ = rfc2865.UserName_SetString(p, userName)
	_ = rfc2865.NASIdentifier_SetString(p, "edge-01")
	_ = rfc2865.CalledStationID_SetString(p, "00-11-22-33-44-55:TestSSID")
	_ = rfc2869.EAPMessage_Set(p, eapMsg)
	_ = rfc2865.State_Set(p, state)
	for _, v := range e.stamps {
		p.Add(rfc2865.VendorSpecific_Type, vsa(v))
	}
	radiussrv.SetMessageAuthenticator(p, []byte(itSecret), p.Authenticator)
	e.lastReq = p
}

// sendRaw は符号化した要求を conn から送り、応答を読む。
func sendRaw(t *testing.T, conn net.PacketConn, addr string, raw []byte) *radius.Packet {
	t.Helper()
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.WriteTo(raw, ua); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := radius.Parse(buf[:n], []byte(itSecret))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// identityMessage は EAP-Response/Identity（RFC 3748 の Type 1）を作る。
func identityMessage(id uint8, identity string) []byte {
	b := make([]byte, 5+len(identity))
	b[0], b[1], b[4] = 2, id, 1
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	copy(b[5:], identity)
	return b
}

// challenge は Access-Challenge の EAP-Request/AKA-Challenge を読む。
func challenge(t *testing.T, resp *radius.Packet) (*eapaka.Packet, []byte) {
	t.Helper()
	if resp.Code != radius.CodeAccessChallenge {
		t.Fatalf("response = %v, want Access-Challenge", resp.Code)
	}
	msg, err := rfc2869.EAPMessage_Lookup(resp)
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := eapaka.Parse(msg)
	if err != nil {
		t.Fatalf("parse EAP: %v", err)
	}
	if pkt.Subtype != eapaka.SubtypeChallenge {
		t.Fatalf("subtype = %d, want Challenge", pkt.Subtype)
	}
	state, err := rfc2865.State_Lookup(resp)
	if err != nil {
		t.Fatal(err)
	}
	return pkt, state
}

func attr[T eapaka.Attribute](pkt *eapaka.Packet) (T, bool) {
	for _, a := range pkt.Attributes {
		if v, ok := a.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// peerKeys は端末の側の K_aut と MSK を、偽の取得先が返したベクターから計算する。
func peerKeys(t *testing.T, identity string, prime bool, v sourcetest.Vector) (kAut, msk []byte) {
	t.Helper()
	if !prime {
		k := eapaka.DeriveKeysAKA(identity, v.CK, v.IK)
		return k.K_aut, k.MSK
	}
	ckp, ikp, err := eapaka.DeriveCKPrimeIKPrime(v.CK, v.IK, "WLAN", v.AUTN)
	if err != nil {
		t.Fatal(err)
	}
	k := eapaka.DeriveKeysAKAPrime(identity, ckp, ikp)
	return k.K_aut, k.MSK
}

// challengeResponse は端末の EAP-Response/AKA-Challenge を作る。
func challengeResponse(t *testing.T, id uint8, prime bool, kAut, res []byte) []byte {
	t.Helper()
	typ := eapaka.TypeAKA
	attrs := []eapaka.Attribute{&eapaka.AtRes{Res: res}}
	if prime {
		typ = eapaka.TypeAKAPrime
		attrs = append(attrs, &eapaka.AtKdf{KDF: eapaka.KDFAKAPrimeWithCKIK})
	}
	attrs = append(attrs, &eapaka.AtMac{MAC: make([]byte, 16)})
	pkt := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: id, Type: typ, Subtype: eapaka.SubtypeChallenge, Attributes: attrs}
	if err := pkt.CalculateAndSetMac(kAut); err != nil {
		t.Fatal(err)
	}
	data, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// checkAccept は Access-Accept と、MS-MPPE の鍵が MSK から作られていることを確かめる。
func checkAccept(t *testing.T, req, resp *radius.Packet, msk []byte) {
	t.Helper()
	if resp.Code != radius.CodeAccessAccept {
		t.Fatalf("response = %v, want Access-Accept", resp.Code)
	}
	recv := microsoft.MSMPPERecvKey_Get(resp, req)
	send := microsoft.MSMPPESendKey_Get(resp, req)
	if !bytes.Equal(recv, msk[:32]) || !bytes.Equal(send, msk[32:64]) {
		t.Error("MS-MPPE keys are not derived from the MSK")
	}
}

func TestIntegration_FullAuth(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		prime  bool
	}{
		{"EAP-AKA", "0", false},
		{"EAP-AKA'", "6", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := startAuth(t)
			identity := tt.prefix + itIMSI + itRealm

			pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))
			v := env.fake.Vectors()[0]
			if r, ok := attr[*eapaka.AtRand](pkt); !ok || !bytes.Equal(r.Rand, v.RAND) {
				t.Fatal("AT_RAND is not the RAND of the vector")
			}
			if tt.prime {
				// AT_KDF_INPUT は anId と同じネットワーク名（契約 §6.4）
				if in, ok := attr[*eapaka.AtKdfInput](pkt); !ok || in.NetworkName != "WLAN" {
					t.Errorf("AT_KDF_INPUT = %+v", in)
				}
			}
			kAut, msk := peerKeys(t, identity, tt.prime, v)
			if ok, err := pkt.VerifyMac(kAut); err != nil || !ok {
				t.Fatalf("AT_MAC of the challenge does not verify: %v", err)
			}

			resp := env.exchange(t, identity, challengeResponse(t, pkt.Identifier, tt.prime, kAut, v.XRES), state)
			checkAccept(t, env.lastReq, resp, msk)

			reqs := env.fake.Requests()
			if len(reqs) != 1 {
				t.Fatalf("source got %d requests, want 1", len(reqs))
			}
			if reqs[0].TraceID != string(state) {
				t.Errorf("X-Trace-ID = %q, want the State %q", reqs[0].TraceID, state)
			}
		})
	}
}

func TestIntegration_Resync(t *testing.T) {
	// 端末の Synchronization-Failure で 1 回だけ取り直し、新しい Challenge で認証が通る。2 回目は拒否する。
	for _, prime := range []bool{false, true} {
		name, prefix, typ := "EAP-AKA", "0", eapaka.TypeAKA
		if prime {
			name, prefix, typ = "EAP-AKA'", "6", eapaka.TypeAKAPrime
		}
		t.Run(name, func(t *testing.T) {
			env := startAuth(t)
			identity := prefix + itIMSI + itRealm
			pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))

			auts := bytes.Repeat([]byte{0x5A}, 14)
			syncFail := func(id uint8) []byte {
				p := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: id, Type: typ, Subtype: eapaka.SubtypeSynchronizationFailure,
					Attributes: []eapaka.Attribute{&eapaka.AtAuts{Auts: auts}}}
				if prime {
					p.Attributes = append(p.Attributes, &eapaka.AtKdf{KDF: eapaka.KDFAKAPrimeWithCKIK})
				}
				data, _ := p.Marshal()
				return data
			}
			pkt2, state2 := challenge(t, env.exchange(t, identity, syncFail(pkt.Identifier), state))
			reqs := env.fake.Requests()
			if len(reqs) != 2 {
				t.Fatalf("source got %d requests, want 2", len(reqs))
			}
			ri, _ := reqs[1].Body["resynchronizationInfo"].(map[string]any)
			first := env.fake.Vectors()[0]
			if ri["rand"] != hex.EncodeToString(first.RAND) || ri["auts"] != hex.EncodeToString(auts) {
				t.Errorf("resynchronizationInfo = %v", ri)
			}

			// 2 回目の Synchronization-Failure は、取得先を呼ばずに拒否する
			resp := env.exchange(t, identity, syncFail(pkt2.Identifier), state2)
			if resp.Code != radius.CodeAccessReject {
				t.Errorf("second resync: response = %v, want Access-Reject", resp.Code)
			}
			if n := len(env.fake.Requests()); n != 2 {
				t.Errorf("source got %d requests, want 2", n)
			}
		})
	}

	t.Run("再同期の後に認証が通る", func(t *testing.T) {
		env := startAuth(t)
		identity := "0" + itIMSI + itRealm
		pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))
		sf := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: pkt.Identifier, Type: eapaka.TypeAKA,
			Subtype: eapaka.SubtypeSynchronizationFailure, Attributes: []eapaka.Attribute{&eapaka.AtAuts{Auts: make([]byte, 14)}}}
		data, _ := sf.Marshal()
		pkt2, state2 := challenge(t, env.exchange(t, identity, data, state))
		v := env.fake.Vectors()[1]
		kAut, msk := peerKeys(t, identity, false, v)
		resp := env.exchange(t, identity, challengeResponse(t, pkt2.Identifier, false, kAut, v.XRES), state2)
		checkAccept(t, env.lastReq, resp, msk)
	})
}

func TestIntegration_Rejects(t *testing.T) {
	t.Run("取得先が拒否した加入者", func(t *testing.T) {
		env := startAuth(t)
		env.fake.AllowOnly() // 403 の一般的な拒否
		identity := "0" + itIMSI + itRealm
		resp := env.exchange(t, identity, identityMessage(1, identity), nil)
		if resp.Code != radius.CodeAccessReject {
			t.Fatalf("response = %v, want Access-Reject", resp.Code)
		}
		if msg, err := rfc2869.EAPMessage_Lookup(resp); err != nil || len(msg) < 4 || msg[0] != 4 {
			t.Error("Access-Reject has no EAP-Failure")
		}
	})

	t.Run("RES が違えば拒否する", func(t *testing.T) {
		env := startAuth(t)
		identity := "0" + itIMSI + itRealm
		pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))
		v := env.fake.Vectors()[0]
		kAut, _ := peerKeys(t, identity, false, v)
		wrong := bytes.Repeat([]byte{0xFF}, len(v.XRES))
		if resp := env.exchange(t, identity, challengeResponse(t, pkt.Identifier, false, kAut, wrong), state); resp.Code != radius.CodeAccessReject {
			t.Errorf("response = %v, want Access-Reject", resp.Code)
		}
	})
}

// rejectBeforeVector は、最初の要求が拒否され、取得先が呼ばれないことを確かめる（MVP の設計概要 §8.1。決定事項 4）。
func rejectBeforeVector(t *testing.T, env *itEnv) {
	t.Helper()
	identity := "0" + itIMSI + itRealm
	resp := env.exchange(t, identity, identityMessage(1, identity), nil)
	if resp.Code != radius.CodeAccessReject {
		t.Fatalf("response = %v, want Access-Reject", resp.Code)
	}
	if msg, err := rfc2869.EAPMessage_Lookup(resp); err != nil || len(msg) < 4 || msg[0] != 4 {
		t.Error("Access-Reject has no EAP-Failure")
	}
	if n := len(env.fake.Requests()); n != 0 {
		t.Errorf("source was called %d times", n)
	}
}

func TestIntegration_RejectBeforeVector(t *testing.T) {
	setState := func(st subscriber.State) func(*itEnv) {
		return func(env *itEnv) {
			if st == subscriber.StateTerminated {
				_, _, _ = env.subs.SetState(context.Background(), itIMSI, subscriber.StateInactive)
			}
			if _, _, err := env.subs.SetState(context.Background(), itIMSI, st); err != nil {
				panic(err)
			}
		}
	}
	tests := []struct {
		name  string
		setup func(*itEnv)
	}{
		{"未登録", func(env *itEnv) { env.mr.Del(subscriber.Key(itIMSI)) }},
		{"本PoC の形の sub:", func(env *itEnv) {
			env.mr.Del(subscriber.Key(itIMSI))
			env.mr.HSet(subscriber.Key(itIMSI), "ki", "00", "source_id", "vecgen")
		}},
		{"inactive", setState(subscriber.StateInactive)},
		{"terminated", setState(subscriber.StateTerminated)},
		{"刻印がない", func(env *itEnv) { env.stamps = nil }},
		{"刻印が 2 つ", func(env *itEnv) { env.stamps = []string{"42", "42"} }},
		{"刻印が数でない", func(env *itEnv) { env.stamps = []string{"+42"} }},
		{"刻印を信頼しない送信元", func(env *itEnv) { env.mr.HSet("client:127.0.0.1", "trust_stamp", "0") }},
		{"テナントの違うルール", func(env *itEnv) { env.stamps = []string{"43"} }},
		{"SSID の違うルール", func(env *itEnv) {
			rules := []subscriber.Rule{{TenantID: itTenant, NasID: "*", SSIDs: []string{"Other"}}}
			if err := env.subs.Update(context.Background(), itIMSI, subscriber.Patch{Rules: &rules}); err != nil {
				panic(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := startAuth(t)
			tt.setup(env)
			rejectBeforeVector(t, env)
		})
	}
}

func TestIntegration_AcceptAttributesAndSession(t *testing.T) {
	env := startAuth(t)
	identity := "0" + itIMSI + itRealm
	pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))
	v := env.fake.Vectors()[0]
	kAut, msk := peerKeys(t, identity, false, v)
	// エッジは同じソケット（同じ送信元のポート）から送り直すので、最後の要求は 1 つのソケットから 2 回送る
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	env.stampsOnly(t, identity, challengeResponse(t, pkt.Identifier, false, kAut, v.XRES), state)
	raw, err := env.lastReq.Encode()
	if err != nil {
		t.Fatal(err)
	}
	final := sendRaw(t, conn, env.addr, raw)
	checkAccept(t, env.lastReq, final, msk)
	if got := rfc2865.SessionTimeout_Get(final); got != 3600 {
		t.Errorf("Session-Timeout = %d", got)
	}
	// セッションに刻印のテナントが残る
	var found bool
	for _, k := range env.mr.Keys() {
		if len(k) > 5 && k[:5] == "sess:" {
			found = true
			if got := env.mr.HGet(k, "tenant_id"); got != "42" {
				t.Errorf("%s tenant_id = %q", k, got)
			}
		}
	}
	if !found {
		t.Error("no session")
	}

	// 最後の要求の再送には、同じ Access-Accept を送り直す（重複応答のキャッシュ。MVP の設計概要 §9）
	again := sendRaw(t, conn, env.addr, raw)
	if again.Code != radius.CodeAccessAccept || again.Identifier != final.Identifier {
		t.Fatalf("replayed response = %v", again.Code)
	}
	a, _ := final.Encode()
	b, _ := again.Encode()
	if !bytes.Equal(a, b) {
		t.Error("replayed response differs")
	}
}

func TestIntegration_StateCheckedBeforeAccept(t *testing.T) {
	// Challenge を送った後に inactive にしたら、Accept の前の確かめで拒否する（MVP の設計概要 §8.1 の 6）
	env := startAuth(t)
	identity := "0" + itIMSI + itRealm
	pkt, state := challenge(t, env.exchange(t, identity, identityMessage(1, identity), nil))
	if _, _, err := env.subs.SetState(context.Background(), itIMSI, subscriber.StateInactive); err != nil {
		t.Fatal(err)
	}
	v := env.fake.Vectors()[0]
	kAut, _ := peerKeys(t, identity, false, v)
	if resp := env.exchange(t, identity, challengeResponse(t, pkt.Identifier, false, kAut, v.XRES), state); resp.Code != radius.CodeAccessReject {
		t.Errorf("response = %v, want Access-Reject", resp.Code)
	}
}

func TestIntegration_IdentityFromEAP(t *testing.T) {
	t.Run("User-Name ではなく EAP の Identity を使う", func(t *testing.T) {
		env := startAuth(t)
		identity := "0" + itIMSI + itRealm
		// User-Name は外側の名前（匿名）。IMSI と鍵の導出は EAP-Response/Identity の Identity から
		pkt, state := challenge(t, env.exchange(t, "anonymous"+itRealm, identityMessage(1, identity), nil))
		v := env.fake.Vectors()[0]
		kAut, msk := peerKeys(t, identity, false, v)
		resp := env.exchange(t, "anonymous"+itRealm, challengeResponse(t, pkt.Identifier, false, kAut, v.XRES), state)
		checkAccept(t, env.lastReq, resp, msk)
	})

	t.Run("仮名の後の AT_IDENTITY", func(t *testing.T) {
		env := startAuth(t)
		pseudonym := "2pseudo" + itRealm
		resp := env.exchange(t, pseudonym, identityMessage(1, pseudonym), nil)
		if resp.Code != radius.CodeAccessChallenge {
			t.Fatalf("response = %v, want Access-Challenge (AKA-Identity)", resp.Code)
		}
		msg, _ := rfc2869.EAPMessage_Lookup(resp)
		req, err := eapaka.Parse(msg)
		if err != nil || req.Subtype != eapaka.SubtypeIdentity {
			t.Fatalf("AKA-Identity request: %v %+v", err, req)
		}
		state, _ := rfc2865.State_Lookup(resp)
		permanent := "0" + itIMSI + itRealm
		idResp := &eapaka.Packet{Code: eapaka.CodeResponse, Identifier: req.Identifier, Type: eapaka.TypeAKA,
			Subtype: eapaka.SubtypeIdentity, Attributes: []eapaka.Attribute{&eapaka.AtIdentity{Identity: permanent}}}
		data, _ := idResp.Marshal()
		pkt, state2 := challenge(t, env.exchange(t, pseudonym, data, state))
		v := env.fake.Vectors()[0]
		// 鍵の導出は AT_IDENTITY の Identity（RFC 4187 §7）
		kAut, msk := peerKeys(t, permanent, false, v)
		final := env.exchange(t, pseudonym, challengeResponse(t, pkt.Identifier, false, kAut, v.XRES), state2)
		checkAccept(t, env.lastReq, final, msk)
	})
}
