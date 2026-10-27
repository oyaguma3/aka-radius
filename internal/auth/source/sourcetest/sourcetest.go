// Package sourcetest は、試験のための偽の取得先（認証ベクターAPI の契約どおりに mTLS で応える）と、
// その登録簿のファイルを作る。試験だけで使う。
package sourcetest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	eapaka "github.com/oyaguma3/go-eapaka"
)

// Request は偽の取得先が受けた要求。
type Request struct {
	IMSI     string
	PathType string // eap-aka / eap-aka-prime
	TraceID  string
	Body     map[string]any
}

// Vector は偽の取得先が返したベクター（試験の側で RES と鍵を計算するため。CK・IK は導出前の値）。
type Vector struct {
	RAND, AUTN, XRES, CK, IK []byte
}

// Server は偽の取得先。
type Server struct {
	// ID は登録簿での取得先の ID。
	ID string
	// BaseURL は基底の URL（https://127.0.0.1:<port>/nudm-ueau/v1）。
	BaseURL string
	// Dir は登録簿のファイルと証明書を置いたディレクトリ。
	Dir string
	// RegistryFile は登録簿のファイル（この取得先 1 つ）。
	RegistryFile string

	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	vectors  []Vector
	handler  http.HandlerFunc
	// Subscribers は払い出しを許す IMSI。nil ならすべて許す。
	subscribers map[string]bool
}

var pathPattern = regexp.MustCompile(`^/nudm-ueau/v1/imsi-([0-9]{5,15})/hss-security-information/(eap-aka|eap-aka-prime)/generate-av$`)

// Start は偽の取得先を立て、登録簿のファイル（timeout は 1 秒）を作る。試験の終わりに止める。
func Start(t testing.TB, id string) *Server {
	t.Helper()
	dir := t.TempDir()
	serverCert, serverPEM := selfSigned(t, "fake-source", false)
	clientCert, clientPEM := selfSigned(t, "aka-radius", true)
	writePEM(t, filepath.Join(dir, "server-ca.pem"), "CERTIFICATE", serverPEM.cert)
	writePEM(t, filepath.Join(dir, "client.pem"), "CERTIFICATE", clientPEM.cert)
	writePEM(t, filepath.Join(dir, "client.key"), "PRIVATE KEY", clientPEM.key)

	clientPool := x509.NewCertPool()
	clientPool.AddCert(clientCert.Leaf)

	s := &Server{ID: id, Dir: dir}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
		MinVersion:   tls.VersionTLS12,
	}
	s.srv.StartTLS()
	t.Cleanup(s.srv.Close)
	s.BaseURL = s.srv.URL + "/nudm-ueau/v1"
	s.RegistryFile = s.WriteRegistry(t, "1s")
	return s
}

// WriteRegistry は、この取得先 1 つの登録簿のファイル（sources.json）を書き、そのパスを返す。
func (s *Server) WriteRegistry(t testing.TB, timeout string) string {
	t.Helper()
	f := map[string]any{"sources": []map[string]any{{
		"id": s.ID, "name": "fake " + s.ID, "baseUrl": s.BaseURL,
		"clientCertFile": "client.pem", "clientKeyFile": "client.key", "serverCaFile": "server-ca.pem",
		"timeout": timeout,
	}}}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "sources.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// SetHandler は応答を差し替える（契約に合わない応答の試験など）。nil で既定の応答に戻す。
func (s *Server) SetHandler(h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// AllowOnly は払い出しを許す IMSI を限る（ほかは 403 の一般的な拒否）。
func (s *Server) AllowOnly(imsis ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers = map[string]bool{}
	for _, i := range imsis {
		s.subscribers[i] = true
	}
}

// Requests は受けた要求を返す。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Vectors は返したベクターを、返した順に返す。
func (s *Server) Vectors() []Vector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Vector(nil), s.vectors...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	m := pathPattern.FindStringSubmatch(r.URL.Path)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	req := Request{TraceID: r.Header.Get("X-Trace-ID"), Body: body}
	if m != nil {
		req.IMSI, req.PathType = m[1], m[2]
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	h := s.handler
	allowed := s.subscribers == nil || s.subscribers[req.IMSI]
	s.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}

	if r.Method != http.MethodPost || m == nil {
		http.NotFound(w, r)
		return
	}
	authType, _ := body["hssAuthType"].(string)
	anID, _ := body["anId"].(string)
	if (req.PathType == "eap-aka" && authType != "EAP_AKA") || (req.PathType == "eap-aka-prime" && authType != "EAP_AKA_PRIME") {
		Problem(w, http.StatusBadRequest, "MANDATORY_IE_INCORRECT")
		return
	}
	if authType == "EAP_AKA_PRIME" && anID != "WLAN" {
		Problem(w, http.StatusBadRequest, "OPTIONAL_IE_INCORRECT")
		return
	}
	if !allowed {
		Problem(w, http.StatusForbidden, "AUTHENTICATION_REJECTED")
		return
	}

	s.mu.Lock()
	v := makeVector(len(s.vectors))
	s.vectors = append(s.vectors, v)
	s.mu.Unlock()
	item := map[string]string{
		"rand": hex.EncodeToString(v.RAND), "autn": hex.EncodeToString(v.AUTN), "xres": hex.EncodeToString(v.XRES),
	}
	if authType == "EAP_AKA_PRIME" {
		ckp, ikp, err := eapaka.DeriveCKPrimeIKPrime(v.CK, v.IK, anID, v.AUTN)
		if err != nil {
			Problem(w, http.StatusInternalServerError, "SYSTEM_FAILURE")
			return
		}
		item["avType"], item["ckPrime"], item["ikPrime"] = "EAP_AKA_PRIME", hex.EncodeToString(ckp), hex.EncodeToString(ikp)
	} else {
		item["avType"], item["ck"], item["ik"] = "EAP_AKA", hex.EncodeToString(v.CK), hex.EncodeToString(v.IK)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"hssAuthenticationVectors": []any{item}})
}

// Problem は契約の ProblemDetails を返す。
func Problem(w http.ResponseWriter, status int, cause string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"title":%q,"status":%d,"cause":%q}`, http.StatusText(status), status, cause)
}

// makeVector は n 番目のベクターを決まった値で作る（RAND が毎回違う）。
func makeVector(n int) Vector {
	fill := func(size int, base byte) []byte {
		b := make([]byte, size)
		for i := range b {
			b[i] = base + byte(i) + byte(n)*7
		}
		return b
	}
	v := Vector{RAND: fill(16, 0x01), AUTN: fill(16, 0x20), XRES: fill(8, 0x40), CK: fill(16, 0x60), IK: fill(16, 0x80)}
	v.AUTN[6] |= 0x80 // AMF の separation bit（EAP-AKA' で要る。RFC 9048）
	return v
}

type pemPair struct{ cert, key []byte }

// selfSigned は自己署名の証明書を作る（サーバーは SAN に 127.0.0.1 と localhost）。
func selfSigned(t testing.TB, cn string, client bool) (tls.Certificate, pemPair) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pemPair{cert: der, key: keyDER}
}

func writePEM(t testing.TB, path, typ string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
