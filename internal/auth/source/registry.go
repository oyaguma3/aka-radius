package source

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 登録簿の値の範囲
const (
	defaultTimeout = 3 * time.Second // 要求 1 回のタイムアウトの既定（契約 §11.1）
	minTimeout     = 100 * time.Millisecond
	maxTimeout     = 30 * time.Second
	// basePathSuffix は基底の URL のパスの終わり（契約 §5）。
	basePathSuffix = "/nudm-ueau/v1"
)

// idPattern は取得先の ID の形（英小文字・数字・-。先頭は英小文字か数字）。
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// fileFormat は登録簿のファイル（JSON）の形。MVP の設計概要 §7.1。
type fileFormat struct {
	Sources []entryFormat `json:"sources"`
}

type entryFormat struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	BaseURL        string `json:"baseUrl"`
	ClientCertFile string `json:"clientCertFile"`
	ClientKeyFile  string `json:"clientKeyFile"`
	ServerCAFile   string `json:"serverCaFile"`
	// Timeout は Go の時間の書き方（例 "3s"、"1500ms"）。空なら 3 秒。
	Timeout string `json:"timeout"`
}

// Info は登録簿の 1 件の情報（ログと管理API 用。鍵のパスは持たない）。
type Info struct {
	ID       string
	Name     string
	BaseURL  string
	Timeout  time.Duration
	NotAfter time.Time // クライアント証明書の期限
}

// entry は読み込んだ 1 件。
type entry struct {
	info   Info
	client *Client
}

// set はある時点の登録簿。読み直しのときは丸ごと差し替える。
type set struct {
	byID  map[string]*entry
	order []string
}

// Registry は取得先の登録簿。ファイルから読み、SIGHUP で読み直す（Reload）。
type Registry struct {
	path string
	cur  atomic.Pointer[set]
	mu   sync.Mutex // Reload を 1 つずつにする
}

// LoadRegistry は path の登録簿を読む。読めない・形が違うときはエラーを返す。
func LoadRegistry(path string) (*Registry, error) {
	s, err := readSet(path)
	if err != nil {
		return nil, err
	}
	r := &Registry{path: path}
	r.cur.Store(s)
	return r, nil
}

// Reload は登録簿を読み直す。読めない・形が違うときは前の登録簿のまま続け、エラーを返す。
// 読み直したときは全部の取得先のクライアントを作り直す（遮断器は閉じた状態から。古い接続は閉じる）。
// 加入者が指している取得先を消す読み直しを拒否するのは段階 2-3（取得先ごとの加入者の数ができてから）。
func (r *Registry) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := readSet(r.path)
	if err != nil {
		return err
	}
	old := r.cur.Swap(s)
	for _, e := range old.byID {
		e.client.close()
	}
	return nil
}

// Get は ID の取得先のクライアントを返す。
func (r *Registry) Get(id string) (*Client, bool) {
	e, ok := r.cur.Load().byID[id]
	if !ok {
		return nil, false
	}
	return e.client, true
}

// List は登録簿の取得先の情報を、ファイルの順に返す。
func (r *Registry) List() []Info {
	s := r.cur.Load()
	out := make([]Info, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id].info)
	}
	return out
}

// readSet はファイルを読み、各取得先の証明書を読み込んでクライアントを作る。
// 証明書と鍵のパスが相対なら、登録簿のファイルのディレクトリから見る。
func readSet(path string) (*set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sources file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// 知らない項目は綴りの誤りとして拒否する
	dec.DisallowUnknownFields()
	var f fileFormat
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse sources file: %w", err)
	}
	if dec.More() {
		return nil, errors.New("parse sources file: trailing data")
	}

	dir := filepath.Dir(path)
	s := &set{byID: map[string]*entry{}}
	var errs []error
	for i, ef := range f.Sources {
		e, err := buildEntry(ef, dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("sources[%d] (%q): %w", i, ef.ID, err))
			continue
		}
		if _, dup := s.byID[e.info.ID]; dup {
			errs = append(errs, fmt.Errorf("sources[%d]: duplicate id %q", i, ef.ID))
			continue
		}
		s.byID[e.info.ID] = e
		s.order = append(s.order, e.info.ID)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return s, nil
}

func buildEntry(ef entryFormat, dir string) (*entry, error) {
	if !idPattern.MatchString(ef.ID) {
		return nil, errors.New("id must be 1-32 characters of a-z, 0-9 and - (not starting with -)")
	}
	baseURL, err := checkBaseURL(ef.BaseURL)
	if err != nil {
		return nil, err
	}
	timeout := defaultTimeout
	if ef.Timeout != "" {
		if timeout, err = time.ParseDuration(ef.Timeout); err != nil || timeout < minTimeout || timeout > maxTimeout {
			return nil, fmt.Errorf("timeout must be a duration between %s and %s", minTimeout, maxTimeout)
		}
	}
	if ef.ClientCertFile == "" || ef.ClientKeyFile == "" || ef.ServerCAFile == "" {
		return nil, errors.New("clientCertFile, clientKeyFile and serverCaFile are required")
	}
	cert, err := tls.LoadX509KeyPair(resolve(dir, ef.ClientCertFile), resolve(dir, ef.ClientKeyFile))
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}
	cas, err := loadCAs(resolve(dir, ef.ServerCAFile))
	if err != nil {
		return nil, err
	}
	name := ef.Name
	if name == "" {
		name = ef.ID
	}
	info := Info{ID: ef.ID, Name: name, BaseURL: baseURL, Timeout: timeout}
	if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
		info.NotAfter = leaf.NotAfter
	}
	return &entry{
		info:   info,
		client: newClient(clientOptions{ID: ef.ID, BaseURL: baseURL, ClientCert: cert, ServerCAs: cas, Timeout: timeout}),
	}, nil
}

// checkBaseURL は基底の URL が https://<ホスト>[:<ポート>]/…/nudm-ueau/v1 の形かを確かめる（契約 §5。平文の HTTP は使わない）。
func checkBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("baseUrl must be https://<host>[:<port>]/nudm-ueau/v1")
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), basePathSuffix) {
		return "", fmt.Errorf("baseUrl path must end with %s", basePathSuffix)
	}
	return strings.TrimRight(raw, "/"), nil
}

// loadCAs は取得先のサーバーの CA（PEM。複数可）を読む。aka-vecgen では自己署名のサーバー証明書そのもの。
func loadCAs(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read server CA: %w", err)
	}
	pool := x509.NewCertPool()
	n := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse server CA: %w", err)
		}
		pool.AddCert(c)
		n++
	}
	if n == 0 {
		return nil, errors.New("server CA has no certificate")
	}
	return pool, nil
}

func resolve(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}
