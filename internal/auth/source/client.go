package source

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sony/gobreaker"
)

// 遮断器の設定（MVP の設計概要 §7.2。取得先ごとに 1 つ）
const (
	cbMaxRequests      = 3                // 半開きのときに通す要求の数
	cbTimeout          = 30 * time.Second // 開いてから半開きにするまでの時間
	cbFailureThreshold = 5                // 開くまでの連続の失敗の数
)

// 接続の設定
const (
	// maxIdleConnsPerHost は取得先ごとに保つ何もしない接続の数の上限（keep-alive。契約 §5）。
	maxIdleConnsPerHost = 64
	// idleConnTimeout は何もしない接続を閉じるまでの時間。取得先は 60 秒は閉じない（契約 §5）ので、それより短くする。
	idleConnTimeout = 50 * time.Second
	// maxResponseBytes は応答の本文の上限（ベクター 1 つの応答は 1 KiB に満たない）。
	maxResponseBytes = 64 << 10
)

// Client は 1 つの取得先のクライアント。接続（keep-alive）と遮断器を持つ。
type Client struct {
	id        string
	baseURL   string
	http      *http.Client
	transport *http.Transport
	cb        *gobreaker.CircuitBreaker
}

// clientOptions は Client を作るための設定（登録簿の 1 件から作る）。
type clientOptions struct {
	ID         string
	BaseURL    string
	ClientCert tls.Certificate
	ServerCAs  *x509.CertPool
	Timeout    time.Duration
}

// newClient は取得先のクライアントを作る。
// 取得先のサーバー証明書は、登録したサーバーの CA のチェーンとホスト名（SAN）で確かめる。システムの CA の束は使わない（契約 §5）。
func newClient(o clientOptions) *Client {
	transport := &http.Transport{
		Proxy: nil, // 取得先へは直接つなぐ
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			RootCAs:      o.ServerCAs,
			Certificates: []tls.Certificate{o.ClientCert},
		},
		// HTTP/1.1 で話す（契約 §5。HTTP/2 は任意なので使わない）
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        maxIdleConnsPerHost,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
		TLSHandshakeTimeout: o.Timeout,
	}
	c := &Client{
		id:        o.ID,
		baseURL:   strings.TrimRight(o.BaseURL, "/"),
		transport: transport,
		// 要求 1 回のタイムアウトは接続を含む（契約 §11.1）。再試行はしない
		http: &http.Client{Transport: transport, Timeout: o.Timeout},
	}
	c.cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        o.ID,
		MaxRequests: cbMaxRequests,
		// 閉じている間は数を数え直さない（0）。数え直すと、タイムアウトのように 1 回に数秒かかる失敗が
		// 間隔の中で 5 回に届かず、いつまでも開かない。連続の失敗の数は、成功すればゼロに戻る
		Interval: 0,
		Timeout:  cbTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= cbFailureThreshold
		},
		// 確定した拒否（加入者がいない・止めてあるなど）は取得先の障害ではないので数えない
		IsSuccessful: func(err error) bool {
			return err == nil || KindOf(err) == KindRejected
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			attrs := []any{"source_id", name, "from", from.String(), "to", to.String()}
			switch to {
			case gobreaker.StateOpen:
				slog.Warn("取得先の遮断器が開いた", append([]any{"event_id", "CB_OPEN"}, attrs...)...)
			case gobreaker.StateHalfOpen:
				slog.Info("取得先の遮断器が半開きになった", append([]any{"event_id", "CB_HALF_OPEN"}, attrs...)...)
			case gobreaker.StateClosed:
				slog.Info("取得先の遮断器が閉じた", append([]any{"event_id", "CB_CLOSE"}, attrs...)...)
			}
		},
	})
	return c
}

// close は何もしない接続を閉じる（登録簿を読み直して使わなくなったとき）。
func (c *Client) close() {
	c.transport.CloseIdleConnections()
}

// State は遮断器の状態を返す（closed / half-open / open）。
func (c *Client) State() string {
	return c.cb.State().String()
}

// avRequest は GenerateAv の要求の本文（契約 §6.2）。
type avRequest struct {
	HssAuthType           string      `json:"hssAuthType"`
	NumOfRequestedVectors int         `json:"numOfRequestedVectors"`
	ResynchronizationInfo *resyncInfo `json:"resynchronizationInfo,omitempty"`
	AnID                  string      `json:"anId,omitempty"`
}

type resyncInfo struct {
	RAND string `json:"rand"`
	AUTS string `json:"auts"`
}

// avResponse は GenerateAv の応答の本文（契約 §6.3）。知らない項目は無視する。
type avResponse struct {
	HssAuthenticationVectors []avItem `json:"hssAuthenticationVectors"`
}

type avItem struct {
	AvType  string `json:"avType"`
	RAND    string `json:"rand"`
	XRES    string `json:"xres"`
	AUTN    string `json:"autn"`
	CK      string `json:"ck"`
	IK      string `json:"ik"`
	CKPrime string `json:"ckPrime"`
	IKPrime string `json:"ikPrime"`
}

// problemDetails はエラーの本文（契約 §9.1）。
type problemDetails struct {
	Status int    `json:"status"`
	Cause  string `json:"cause"`
}

// GenerateAV は取得先に 1 つの認証ベクターを要求する（契約 §6）。誤りは *Error で返す。
func (c *Client) GenerateAV(ctx context.Context, req *Request) (*Vector, error) {
	start := time.Now()
	result, err := c.cb.Execute(func() (any, error) {
		return c.generateAV(ctx, req, start)
	})
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return nil, &Error{Kind: KindCircuitOpen, SourceID: c.id, Err: err}
		}
		return nil, err
	}
	return result.(*Vector), nil
}

func (c *Client) generateAV(ctx context.Context, req *Request, start time.Time) (*Vector, error) {
	fail := func(kind Kind, status int, cause, reason string, err error) (*Vector, error) {
		return nil, &Error{Kind: kind, SourceID: c.id, Status: status, Cause: cause, Reason: reason,
			LatencyMs: time.Since(start).Milliseconds(), Err: err}
	}

	body := avRequest{HssAuthType: req.Method.hssAuthType(), NumOfRequestedVectors: 1}
	if req.Method == MethodAKAPrime {
		body.AnID = req.ANID
	}
	if req.Resync != nil {
		body.ResynchronizationInfo = &resyncInfo{RAND: hex.EncodeToString(req.Resync.RAND), AUTS: hex.EncodeToString(req.Resync.AUTS)}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fail(KindTemporary, 0, "", "encode request", err)
	}
	url := c.baseURL + "/imsi-" + req.IMSI + "/hss-security-information/" + req.Method.pathType() + "/generate-av"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fail(KindTemporary, 0, "", "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, application/problem+json")
	if req.TraceID != "" {
		httpReq.Header.Set("X-Trace-ID", req.TraceID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 接続・TLS の失敗とタイムアウト
		reason := "request failed"
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			reason = "timeout"
		}
		return fail(KindTemporary, 0, "", reason, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fail(KindTemporary, resp.StatusCode, "", "read response", err)
	}
	if len(data) > maxResponseBytes {
		return fail(KindContractViolation, resp.StatusCode, "", "response too large", nil)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		v, reason := parseVector(data, req.Method)
		if v == nil {
			return fail(KindContractViolation, resp.StatusCode, "", reason, nil)
		}
		return v, nil
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotImplemented:
		cause, ok := parseProblem(resp.Header.Get("Content-Type"), data)
		if !ok {
			// 本文（ProblemDetails の cause）のない 4xx・501 は契約違反
			return fail(KindContractViolation, resp.StatusCode, "", "error without problem details", nil)
		}
		return fail(KindRejected, resp.StatusCode, cause, "", nil)
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		cause, _ := parseProblem(resp.Header.Get("Content-Type"), data)
		return fail(KindTemporary, resp.StatusCode, cause, "", nil)
	default:
		return fail(KindContractViolation, resp.StatusCode, "", "status not in contract", nil)
	}
}

// parseVector は 200 の本文を読み、要素の数・avType・値の長さを確かめる（契約 §6.3）。
// 合わなければ nil と理由を返す。
func parseVector(data []byte, m Method) (*Vector, string) {
	var r avResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, "invalid json"
	}
	if n := len(r.HssAuthenticationVectors); n != 1 {
		return nil, fmt.Sprintf("want 1 vector, got %d", n)
	}
	item := r.HssAuthenticationVectors[0]
	if item.AvType != m.hssAuthType() {
		return nil, fmt.Sprintf("avType mismatch: want %s, got %q", m.hssAuthType(), item.AvType)
	}
	ck, ik, ckName, ikName := item.CK, item.IK, "ck", "ik"
	if m == MethodAKAPrime {
		ck, ik, ckName, ikName = item.CKPrime, item.IKPrime, "ckPrime", "ikPrime"
	}
	v := &Vector{}
	fields := []struct {
		name     string
		hexValue string
		min, max int
		dst      *[]byte
	}{
		{"rand", item.RAND, 16, 16, &v.RAND},
		{"autn", item.AUTN, 16, 16, &v.AUTN},
		{"xres", item.XRES, 4, 16, &v.XRES},
		{ckName, ck, 16, 16, &v.CK},
		{ikName, ik, 16, 16, &v.IK},
	}
	for _, f := range fields {
		// hex.DecodeString は大文字も受け付ける（契約 §6.2）
		b, err := hex.DecodeString(f.hexValue)
		if err != nil {
			return nil, f.name + " is not hex"
		}
		if len(b) < f.min || len(b) > f.max {
			return nil, fmt.Sprintf("%s has %d bytes", f.name, len(b))
		}
		*f.dst = b
	}
	return v, ""
}

// parseProblem はエラーの本文から cause を取り出す。ProblemDetails でなければ false を返す。
func parseProblem(contentType string, data []byte) (string, bool) {
	mt, _, _ := mime.ParseMediaType(contentType)
	if mt != "application/problem+json" && mt != "application/json" {
		return "", false
	}
	var p problemDetails
	if err := json.Unmarshal(data, &p); err != nil || p.Cause == "" {
		return "", false
	}
	return p.Cause, true
}

// isTimeout は err がタイムアウトかを返す。
func isTimeout(err error) bool {
	var te interface{ Timeout() bool }
	return errors.As(err, &te) && te.Timeout()
}
