package radiussrv

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oyaguma3/aka-radius/internal/envconf"
	"layeh.com/radius"
)

// 要求の期限と同時に処理する上限の既定値（MVP の設計概要 §4。段階 2-3 の G7）。
// 期限は、取得先のタイムアウト（既定 3 秒）より長く、エッジの再送の間隔（5 秒）より短くする。
const (
	DefaultRequestTimeout = 4 * time.Second
	DefaultMaxInFlight    = 256
)

// 重複応答のキャッシュ（MVP の設計概要 §9。段階 2-3 の G8）。
const (
	// replayTTL は応答を持つ時間（エッジの再送の窓 5 秒 × 2 回より長く）
	replayTTL = 30 * time.Second
	// replayMax はキャッシュの件数の上限（見込みの約 5,000 件の 10 倍。超えたら古いものから消す）
	replayMax = 50000
)

// GuardConfig は要求の期限と同時に処理する上限。
type GuardConfig struct {
	// RequestTimeout は 1 つの要求の処理の期限（RADIUS_REQUEST_TIMEOUT）
	RequestTimeout time.Duration
	// MaxInFlight は同時に処理する要求の上限（RADIUS_MAX_INFLIGHT）
	MaxInFlight int
}

// LoadGuardConfig は RADIUS_REQUEST_TIMEOUT と RADIUS_MAX_INFLIGHT を読む（auth と acct で共通）。
func LoadGuardConfig() (GuardConfig, error) {
	timeout, err := envconf.Duration("RADIUS_REQUEST_TIMEOUT", DefaultRequestTimeout, 500*time.Millisecond, 30*time.Second)
	if err != nil {
		return GuardConfig{}, err
	}
	n, err := envconf.PositiveInt("RADIUS_MAX_INFLIGHT", DefaultMaxInFlight)
	if err != nil {
		return GuardConfig{}, err
	}
	return GuardConfig{RequestTimeout: timeout, MaxInFlight: int(n)}, nil
}

// Guard は、RADIUS のハンドラーの前で、重複した要求への応答の再送、要求の期限、同時に処理する上限を受け持つ。
//
//   - 同じ要求（送信元の IP とポート、Identifier、Request Authenticator が同じ。RFC 5080 §2.2.2）の再送には、
//     前に送った応答をそのまま送り直す。処理中の重複は捨てる（最初の要求の応答がキャッシュに入れば、次の再送に返る）。
//   - 上限を超えた要求は、応えずに捨てる（まだ何もしていないので、エッジの再送を待っても害がない）。
//   - 要求の処理には期限つきの context を渡す。期限を過ぎた処理は、取得先のタイムアウトと同じく拒否で終える。
type Guard struct {
	// prefix はログの event_id の接頭辞（AUTH / ACCT）
	prefix  string
	next    radius.Handler
	timeout time.Duration
	sem     chan struct{}
	cache   *replayCache

	// overloaded は捨てた要求の数（ログを 1 秒に 1 回に間引くため）、lastOverloadLog はその最後のログの時刻
	overloaded      atomic.Int64
	lastOverloadLog atomic.Int64
}

// NewGuard は Guard を作る。prefix はログの event_id の接頭辞（AUTH / ACCT）。
func NewGuard(prefix string, cfg GuardConfig, next radius.Handler) *Guard {
	return &Guard{
		prefix:  prefix,
		next:    next,
		timeout: cfg.RequestTimeout,
		sem:     make(chan struct{}, cfg.MaxInFlight),
		cache:   newReplayCache(replayTTL, replayMax),
	}
}

// ServeRADIUS は radius.Handler の実装。
func (g *Guard) ServeRADIUS(w radius.ResponseWriter, r *radius.Request) {
	key := keyOf(r)
	now := time.Now()
	switch resp, st := g.cache.begin(key, now); st {
	case replayCached:
		slog.Info("重複した要求に前の応答を送り直した",
			"event_id", g.prefix+"_DUPLICATE_REPLAYED",
			"src_ip", ExtractIP(r.RemoteAddr),
			"code", r.Code,
			"identifier", r.Identifier,
		)
		if err := w.Write(resp); err != nil {
			slog.Error("RADIUS応答送信失敗", "event_id", "PKT_SEND_ERR", "error", err)
		}
		return
	case replayInFlight:
		slog.Debug("処理中の要求の重複を捨てた",
			"event_id", g.prefix+"_DUPLICATE_DROPPED",
			"src_ip", ExtractIP(r.RemoteAddr),
			"identifier", r.Identifier,
		)
		return
	}

	select {
	case g.sem <- struct{}{}:
	default:
		g.cache.abort(key)
		g.logOverload(now)
		return
	}
	defer func() { <-g.sem }()

	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	cw := &captureWriter{w: w}
	// ハンドラーがパニックしても、キャッシュの処理中の印を残さない（recoverHandler が外側で回復する）
	defer func() {
		if cw.pkt != nil {
			g.cache.finish(key, cw.pkt, time.Now())
		} else {
			g.cache.abort(key)
		}
	}()
	g.next.ServeRADIUS(cw, r.WithContext(ctx))
}

// logOverload は上限を超えて捨てた要求をログに出す（1 秒に 1 回まで。間の数は dropped に入れる）。
func (g *Guard) logOverload(now time.Time) {
	n := g.overloaded.Add(1)
	last := g.lastOverloadLog.Load()
	if now.UnixNano()-last < int64(time.Second) || !g.lastOverloadLog.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	g.overloaded.Add(-n)
	slog.Warn("同時に処理する要求の上限を超えたので捨てた",
		"event_id", "RADIUS_OVERLOAD",
		"server", g.prefix,
		"max_in_flight", cap(g.sem),
		"dropped", n,
	)
}

// captureWriter は送った応答を覚える。
type captureWriter struct {
	w   radius.ResponseWriter
	pkt *radius.Packet
}

func (c *captureWriter) Write(p *radius.Packet) error {
	c.pkt = p
	return c.w.Write(p)
}

// replayKey は要求を見分けるキー。
type replayKey struct {
	addr          string
	identifier    byte
	authenticator [16]byte
}

func keyOf(r *radius.Request) replayKey {
	k := replayKey{identifier: r.Identifier, authenticator: r.Authenticator}
	if r.RemoteAddr != nil {
		k.addr = r.RemoteAddr.String()
	}
	return k
}

type replayState int

const (
	replayNew replayState = iota
	replayInFlight
	replayCached
)

type replayEntry struct {
	// pkt は送った応答（nil なら処理中）。Packet.Encode は Packet を書き換えないので、同じ値を送り直せる
	pkt   *radius.Packet
	added time.Time
}

type queued struct {
	key   replayKey
	added time.Time
}

// replayCache は応答のキャッシュ。持つ時間が一定なので、入れた順（queue）がそのまま期限の順になる。
type replayCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	m     map[replayKey]replayEntry
	queue []queued
	// head は queue の先頭（消した分は詰めずに進め、半分を超えたら詰める）
	head int
}

func newReplayCache(ttl time.Duration, max int) *replayCache {
	return &replayCache{ttl: ttl, max: max, m: make(map[replayKey]replayEntry)}
}

// begin は要求の受け付けを記録する。キャッシュにあれば応答を、処理中なら replayInFlight を返す。
func (c *replayCache) begin(k replayKey, now time.Time) (*radius.Packet, replayState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict(now)
	if e, ok := c.m[k]; ok {
		if e.pkt != nil {
			return e.pkt, replayCached
		}
		return nil, replayInFlight
	}
	c.m[k] = replayEntry{added: now}
	c.queue = append(c.queue, queued{k, now})
	return nil, replayNew
}

// finish は送った応答を記録する。
func (c *replayCache) finish(k replayKey, pkt *radius.Packet, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		e.pkt = pkt
		c.m[k] = e
	}
	c.evict(now)
}

// abort は応答を送らなかった要求の印を消す（次の再送を新しい要求として処理する）。
func (c *replayCache) abort(k replayKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}

// evict は期限を過ぎたものと、上限を超えた分を古いものから消す。
func (c *replayCache) evict(now time.Time) {
	for ; c.head < len(c.queue); c.head++ {
		q := c.queue[c.head]
		if now.Sub(q.added) < c.ttl && len(c.m) <= c.max {
			break
		}
		// abort の後に同じキーで入れ直したものは消さない
		if e, ok := c.m[q.key]; ok && e.added.Equal(q.added) {
			delete(c.m, q.key)
		}
	}
	if c.head > 1024 && c.head > len(c.queue)/2 {
		c.queue = append(c.queue[:0:0], c.queue[c.head:]...)
		c.head = 0
	}
}

// size はキャッシュの件数を返す（試験用）。
func (c *replayCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
