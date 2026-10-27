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
	// lastReq は最後に送った Access-Request（MS-MPPE の鍵の復号に要る）。
	lastReq *radius.Packet
}

// startAuth は miniredis、偽の取得先、auth の RADIUS サーバーを立てる。
// 加入者 itIMSI は偽の取得先を指し、認可ポリシーは既定で許可にする（PoC の policy:。段階 2-3 で sub: に移る）。
func startAuth(t *testing.T) *itEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.HSet("client:127.0.0.1", "secret", itSecret)
	mr.HSet("sub:"+itIMSI, "source_id", "vecgen")
	mr.HSet("policy:"+itIMSI, "default", "allow", "rules", "[]")

	fake := sourcetest.Start(t, "vecgen")
	registry, err := source.LoadRegistry(fake.RegistryFile)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	host, port, _ := net.SplitHostPort(mr.Addr())
	cfg := &config.Config{
		Common:      envconf.Common{ValkeyHost: host, ValkeyPort: port, LogLevel: "INFO"},
		NetworkName: "WLAN",
	}
	vc, err := store.NewValkeyClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vc.Close() })

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
	return &itEnv{mr: mr, fake: fake, addr: cfg.ListenAddr}
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
	radiussrv.SetMessageAuthenticator(p, []byte(itSecret), p.Authenticator)
	e.lastReq = p
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := radius.Exchange(ctx, p, e.addr)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
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

	t.Run("取得先のない加入者は取得先を呼ばない", func(t *testing.T) {
		env := startAuth(t)
		env.mr.Del("sub:" + itIMSI)
		identity := "0" + itIMSI + itRealm
		if resp := env.exchange(t, identity, identityMessage(1, identity), nil); resp.Code != radius.CodeAccessReject {
			t.Fatalf("response = %v, want Access-Reject", resp.Code)
		}
		if n := len(env.fake.Requests()); n != 0 {
			t.Errorf("source was called %d times", n)
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
