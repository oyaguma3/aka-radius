package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newKeyPair は自己署名の証明書と秘密鍵を生成する。
func newKeyPair(t *testing.T, cn string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func validKeyPair(t *testing.T, cn string) tls.Certificate {
	return newKeyPair(t, cn, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func TestParseClients(t *testing.T) {
	fpA := strings.Repeat("a", 64)
	fpB := strings.Repeat("B", 64)
	colon := strings.TrimSuffix(strings.Repeat("CC:", 32), ":")

	got, err := ParseClients("# 管理クライアント\n bff-01=" + fpA + " \n\nbff.02 = " + fpB + "\nops_3=" + colon + "\n")
	if err != nil {
		t.Fatalf("ParseClients() error = %v", err)
	}
	want := Clients{fpA: "bff-01", strings.ToLower(fpB): "bff.02", strings.Repeat("c", 64): "ops_3"}
	if len(got) != len(want) {
		t.Fatalf("ParseClients() = %v, want %v", got, want)
	}
	for fp, name := range want {
		if got[fp] != name {
			t.Errorf("clients[%s] = %q, want %q", fp, got[fp], name)
		}
	}

	if got, err := ParseClients(""); err != nil || len(got) != 0 {
		t.Errorf("ParseClients(\"\") = %v, %v", got, err)
	}

	for _, bad := range []string{
		"bff-01",             // = がない
		"=" + fpA,            // 識別名がない
		"bad name=" + fpA,    // 識別名に空白
		"bff-01=" + fpA[:63], // 63桁
		"bff-01=" + strings.Repeat("g", 64),
		"bff-01=" + fpA + "\nbff-02=" + fpA,     // 重複
		"bff-01=" + fpA + "," + "bff-02=" + fpB, // カンマ区切り（前の環境変数の形）は受けない
	} {
		if _, err := ParseClients(bad); err == nil {
			t.Errorf("ParseClients(%q) expected error", bad)
		}
	}

	// 同じ識別名に新旧 2 つ
	got, err = ParseClients("bff-01=" + fpA + "\nbff-01=" + fpB)
	if err != nil || len(got) != 2 || strings.Join(got.Names(), ",") != "bff-01" {
		t.Errorf("two fingerprints for one name = %v %v", got, err)
	}
}

func TestPinsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-clients")
	a, b := validKeyPair(t, "a"), validKeyPair(t, "b")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadPins(path); err == nil {
		t.Error("LoadPins on a missing file: want error")
	}
	write("# なし\n")
	if _, err := LoadPins(path); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Errorf("LoadPins on an empty file: %v", err)
	}
	write("bff-01=" + Fingerprint(a.Leaf) + "\n")
	p, err := LoadPins(path)
	if err != nil {
		t.Fatal(err)
	}
	names, loaded := p.Names()
	if strings.Join(names, ",") != "bff-01" || loaded.IsZero() || p.Fingerprints() != 1 {
		t.Errorf("Names() = %v %v", names, loaded)
	}
	if _, ok := p.NameOf(b.Leaf); ok {
		t.Error("b accepted before the reload")
	}

	// 読み直すと新しいピンになる
	write("bff-01=" + Fingerprint(a.Leaf) + "\nops-02=" + Fingerprint(b.Leaf) + "\n")
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if name, ok := p.NameOf(b.Leaf); !ok || name != "ops-02" {
		t.Errorf("NameOf(b) = %q %v", name, ok)
	}

	// 形が違う・空のときは前のまま
	for _, bad := range []string{"broken", ""} {
		write(bad)
		if err := p.Reload(); err == nil {
			t.Errorf("Reload(%q): want error", bad)
		}
		if _, ok := p.NameOf(b.Leaf); !ok {
			t.Errorf("Reload(%q) dropped the previous pins", bad)
		}
	}
	if err := NewPins(Clients{}).Reload(); err == nil {
		t.Error("Reload without a file: want error")
	}
}

func TestVerifyConnection(t *testing.T) {
	registered := validKeyPair(t, "bff-01")
	var logBuf bytes.Buffer
	v := &Verifier{
		Pins: NewPins(Clients{Fingerprint(registered.Leaf): "bff-01"}),
		Log:  slog.New(slog.NewJSONHandler(&logBuf, nil)),
	}

	if err := v.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{registered.Leaf}}); err != nil {
		t.Errorf("registered certificate rejected: %v", err)
	}
	if err := v.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Error("no certificate accepted")
	}
	other := validKeyPair(t, "other")
	if err := v.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{other.Leaf}}); err == nil {
		t.Error("unregistered certificate accepted")
	}
	if !strings.Contains(logBuf.String(), Fingerprint(other.Leaf)) || !strings.Contains(logBuf.String(), "ADMIN_CLIENT_REJECTED") {
		t.Errorf("rejection log = %s", logBuf.String())
	}

	// 有効期間外
	for _, now := range []time.Time{registered.Leaf.NotBefore.Add(-time.Minute), registered.Leaf.NotAfter.Add(time.Minute)} {
		v.Now = func() time.Time { return now }
		if err := v.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{registered.Leaf}}); err == nil {
			t.Errorf("certificate accepted at %v", now)
		}
	}

	// ログがなくても動く
	v = &Verifier{Pins: NewPins(Clients{})}
	if err := v.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Error("no certificate accepted")
	}
}

func TestNameFromRequest(t *testing.T) {
	kp := validKeyPair(t, "bff-01")
	clients := NewPins(Clients{Fingerprint(kp.Leaf): "bff-01"})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := clients.NameFromRequest(r); got != "" {
		t.Errorf("NameFromRequest() without TLS = %q", got)
	}
	r.TLS = &tls.ConnectionState{}
	if got := clients.NameFromRequest(r); got != "" {
		t.Errorf("NameFromRequest() without certificate = %q", got)
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{kp.Leaf}}
	if got := clients.NameFromRequest(r); got != "bff-01" {
		t.Errorf("NameFromRequest() = %q, want bff-01", got)
	}
}

// TestTLSHandshake は実際の TLS 接続で、登録済みの証明書だけを受け付けることを確認する（D-13 §9）。
func TestTLSHandshake(t *testing.T) {
	serverCert := validKeyPair(t, "aka-radius-admin")
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key")
	writeKeyPair(t, serverCert, certPath, keyPath)
	certs, err := LoadCertFile(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	registered := validKeyPair(t, "bff-01")
	var logBuf bytes.Buffer
	v := &Verifier{Pins: NewPins(Clients{Fingerprint(registered.Leaf): "bff-01"}), Log: slog.New(slog.NewJSONHandler(&logBuf, nil))}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, v.Pins.NameFromRequest(r))
	}))
	srv.TLS = v.TLSConfig(certs)
	srv.Config.ErrorLog = slog.NewLogLogger(slog.NewJSONHandler(io.Discard, nil), slog.LevelDebug)
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(serverCert.Leaf)
	client := func(certs ...tls.Certificate) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: "localhost", Certificates: certs,
		}}}
	}

	resp, err := client(registered).Get(srv.URL)
	if err != nil {
		t.Fatalf("registered client: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "bff-01" {
		t.Errorf("mgmt client = %q", body)
	}

	if _, err := client(validKeyPair(t, "other")).Get(srv.URL); err == nil {
		t.Error("unregistered client connected")
	}
	if _, err := client().Get(srv.URL); err == nil {
		t.Error("client without certificate connected")
	}
	// 拒否したログに送信元IPを残す
	if n := strings.Count(logBuf.String(), `"src_ip":"127.0.0.1"`); n != 2 {
		t.Errorf("rejection logs with src_ip = %d, want 2: %s", n, logBuf.String())
	}

	// TLS 1.1 以下は受け付けない
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "localhost", Certificates: []tls.Certificate{registered}, MaxVersion: tls.VersionTLS11,
	}}}
	if _, err := old.Get(srv.URL); err == nil {
		t.Error("TLS 1.1 client connected")
	}

	// サーバー証明書を読み直すと、次の接続から新しい証明書を出す（壊れたファイルなら前のまま）
	newCert := validKeyPair(t, "aka-radius-admin-new")
	writeKeyPair(t, newCert, certPath, keyPath)
	if err := certs.Reload(); err != nil {
		t.Fatal(err)
	}
	if certs.Leaf().Subject.CommonName != "aka-radius-admin-new" {
		t.Errorf("Leaf() = %v", certs.Leaf().Subject)
	}
	if _, err := client(registered).Get(srv.URL); err == nil {
		t.Error("client trusting only the old server certificate connected after the reload")
	}
	if err := os.WriteFile(keyPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := certs.Reload(); err == nil || certs.Leaf().Subject.CommonName != "aka-radius-admin-new" {
		t.Errorf("Reload with a broken key: %v, leaf %v", err, certs.Leaf().Subject)
	}
}

// writeKeyPair は証明書と鍵を PEM のファイルに書く。
func writeKeyPair(t *testing.T, kp tls.Certificate, certPath, keyPath string) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(kp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: kp.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
