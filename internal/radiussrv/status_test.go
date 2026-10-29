package radiussrv

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"log/slog"
	"strings"
	"testing"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"
)

// setValidMessageAuthenticator はリクエストパケットに正しいMessage-Authenticatorを設定する
func setValidMessageAuthenticator(p *radius.Packet, secret []byte) {
	zeroMA := make([]byte, 16)
	_ = rfc2869.MessageAuthenticator_Set(p, zeroMA)
	data, _ := p.MarshalBinary()
	mac := hmac.New(md5.New, secret)
	mac.Write(data)
	_ = rfc2869.MessageAuthenticator_Set(p, mac.Sum(nil))
}

func TestHandleStatusServer_Success(t *testing.T) {
	secret := []byte("status-secret")
	req := radius.New(radius.CodeStatusServer, secret)
	setValidMessageAuthenticator(req, secret)

	resp := HandleStatusServer(req, secret, radius.CodeAccessAccept, "RADIUS_STATUS_AUTH_FAIL", "192.168.1.1", "trace-001")

	if resp == nil {
		t.Fatal("HandleStatusServer returned nil for valid request")
	}
	if resp.Code != radius.CodeAccessAccept {
		t.Errorf("Code = %d, want %d", resp.Code, radius.CodeAccessAccept)
	}

	// EAP-Messageが含まれないこと
	if _, err := rfc2869.EAPMessage_Lookup(resp); err == nil {
		t.Error("Status-Server response should not contain EAP-Message")
	}

	// Message-Authenticator存在確認
	_, err := rfc2869.MessageAuthenticator_Lookup(resp)
	if err != nil {
		t.Error("Message-Authenticator not found in response")
	}
}

func TestHandleStatusServer_InvalidAuth(t *testing.T) {
	secret := []byte("status-secret")
	req := radius.New(radius.CodeStatusServer, secret)

	// 不正なMessage-Authenticatorを設定
	invalidMA := make([]byte, 16)
	invalidMA[0] = 0xFF
	_ = rfc2869.MessageAuthenticator_Set(req, invalidMA)

	resp := HandleStatusServer(req, secret, radius.CodeAccessAccept, "RADIUS_STATUS_AUTH_FAIL", "192.168.1.1", "trace-002")

	if resp != nil {
		t.Error("HandleStatusServer should return nil for invalid MA")
	}
}

func TestHandleStatusServer_WithProxyState(t *testing.T) {
	secret := []byte("status-secret")
	req := radius.New(radius.CodeStatusServer, secret)
	_ = rfc2865.ProxyState_Add(req, []byte("proxy-1"))
	_ = rfc2865.ProxyState_Add(req, []byte("proxy-2"))
	setValidMessageAuthenticator(req, secret)

	resp := HandleStatusServer(req, secret, radius.CodeAccessAccept, "RADIUS_STATUS_AUTH_FAIL", "192.168.1.1", "trace-003")

	if resp == nil {
		t.Fatal("HandleStatusServer returned nil for valid request")
	}

	// Proxy-Stateがコピーされていること
	states, err := rfc2865.ProxyState_Gets(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("response has %d ProxyState, want 2", len(states))
	}
	if string(states[0]) != "proxy-1" {
		t.Errorf("ProxyState[0] = %q, want %q", string(states[0]), "proxy-1")
	}
	if string(states[1]) != "proxy-2" {
		t.Errorf("ProxyState[1] = %q, want %q", string(states[1]), "proxy-2")
	}
}

func TestHandleStatusServer_NoMessageAuth(t *testing.T) {
	secret := []byte("status-secret")
	req := radius.New(radius.CodeStatusServer, secret)

	// Message-Authenticator属性なし
	resp := HandleStatusServer(req, secret, radius.CodeAccessAccept, "RADIUS_STATUS_AUTH_FAIL", "192.168.1.1", "trace-004")

	if resp != nil {
		t.Error("HandleStatusServer should return nil when MA is missing")
	}
}

func TestHandleStatusServer_AccountingResponse(t *testing.T) {
	// Accounting の口は Accounting-Response を返す（D-10 §4.3）。
	secret := []byte("acct-secret")
	req := radius.New(radius.CodeStatusServer, secret)
	setValidMessageAuthenticator(req, secret)

	resp := HandleStatusServer(req, secret, radius.CodeAccountingResponse, "RADIUS_AUTH_ERR", "192.168.1.1", "trace-005")
	if resp == nil {
		t.Fatal("HandleStatusServer returned nil for valid request")
	}
	if resp.Code != radius.CodeAccountingResponse {
		t.Errorf("Code = %d, want %d", resp.Code, radius.CodeAccountingResponse)
	}
	if !VerifyMessageAuthenticator(&radius.Packet{Code: resp.Code, Identifier: resp.Identifier, Authenticator: req.Authenticator, Secret: secret, Attributes: resp.Attributes}, secret) {
		t.Error("Message-Authenticator of the response is not computed with the request authenticator")
	}
}

func TestHandleStatusServer_FailEventID(t *testing.T) {
	// Message-Authenticator の誤りは、呼び出し側が渡した event_id で記録する。
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	secret := []byte("acct-secret")
	req := radius.New(radius.CodeStatusServer, secret)
	if resp := HandleStatusServer(req, secret, radius.CodeAccountingResponse, "RADIUS_AUTH_ERR", "192.168.1.1", "trace-006"); resp != nil {
		t.Fatal("HandleStatusServer should return nil when MA is missing")
	}
	if !strings.Contains(buf.String(), `"event_id":"RADIUS_AUTH_ERR"`) {
		t.Errorf("log does not contain the event_id: %s", buf.String())
	}
}
