// Package audit は管理API の監査ログを Valkey の Stream（audit）に書き、読み出す（MVP の設計概要 §13）。
// 項目は aka-vecgen の監査ログにそろえる（時刻、操作者、管理クライアント、操作、対象、変更の内容、トレースID）。
// 日ごとのファイル（JSONL）への書き出しは段階 2-5 で足す。
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// StreamKey は監査ログの Stream のキー（MVP の設計概要 §6.1。本PoC の audit:prov を改めた）。
const StreamKey = "audit"

// storeTimeout は監査ログを Stream に書くときの上限時間。
const storeTimeout = 2 * time.Second

// StreamIDPattern は Stream のエントリID の形式（{ミリ秒}-{連番}）。
var StreamIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}$`)

// Entry は監査ログの 1 件。
type Entry struct {
	// ID は Stream のエントリID（読み出したときだけ）
	ID string
	// Time は記録した時刻（読み出したときはエントリID のミリ秒から求める）
	Time time.Time
	// Operator は X-Operator-Id の値（省略時は空文字）
	Operator string
	// MgmtClient はクライアント証明書に対応する管理クライアントの識別名
	MgmtClient string
	// Action は操作（例 subscriber.create）
	Action string
	// Target は対象（IMSI など）
	Target string
	// Detail は変更の内容（JSON の文字列。なければ空）
	Detail  string
	TraceID string
}

// Recorder は監査ログを Stream に書き、ログ（AUDIT）にも出す。
type Recorder struct {
	rdb *redis.Client
	// maxLen は Stream に持つ件数の上限（おおよそ。超えた分は古いものから捨てる）
	maxLen int64
	log    *slog.Logger
}

// NewRecorder は Recorder を作る。
func NewRecorder(rdb *redis.Client, maxLen int64, log *slog.Logger) *Recorder {
	return &Recorder{rdb: rdb, maxLen: maxLen, log: log}
}

// Record は監査ログを 1 件書く。書けなくても操作は成功として扱い、ログに AUDIT_WRITE_ERR を出す。
func (r *Recorder) Record(ctx context.Context, e Entry) {
	// 要求が途中で切れても書くことは試みる。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer cancel()
	id, err := r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamKey,
		MaxLen: r.maxLen,
		Approx: true,
		Values: []any{
			"operator", e.Operator,
			"mgmt_client", e.MgmtClient,
			"action", e.Action,
			"target", e.Target,
			"detail", e.Detail,
			"trace_id", e.TraceID,
		},
	}).Result()
	if err != nil {
		r.log.Error("failed to write audit log",
			"event_id", "AUDIT_WRITE_ERR",
			"trace_id", e.TraceID,
			"action", e.Action,
			"target", e.Target,
			"error", err.Error(),
		)
		return
	}
	r.log.Info("audit",
		"event_id", "AUDIT",
		"trace_id", e.TraceID,
		"operator", e.Operator,
		"mgmt_client", e.MgmtClient,
		"action", e.Action,
		"target", e.Target,
		"stream_id", id,
	)
}

// List は監査ログを新しい順に limit 件まで返す。before を渡すと、そのエントリID より古いものを返す。
// さらに古いものがあれば、次に before に渡す値（返した最後のエントリID）を next に入れる。
func (r *Recorder) List(ctx context.Context, before string, limit int) (entries []Entry, next string, err error) {
	end := "+"
	if before != "" {
		if end, err = previousID(before); err != nil {
			return nil, "", err
		}
		if end == "" {
			// 0-0 より古いものはない
			return []Entry{}, "", nil
		}
	}
	msgs, err := r.rdb.XRevRangeN(ctx, StreamKey, end, "-", int64(limit)+1).Result()
	if err != nil {
		return nil, "", err
	}
	more := len(msgs) > limit
	if more {
		msgs = msgs[:limit]
	}
	entries = make([]Entry, len(msgs))
	for i, m := range msgs {
		entries[i] = entryFromMessage(m)
	}
	if more {
		next = msgs[len(msgs)-1].ID
	}
	return entries, next, nil
}

// previousID は、エントリID の直前の ID を返す（XREVRANGE の終端に使う。直前がなければ空文字）。
// 排他的な範囲指定（"(" 付き）に頼らず、どの版の Valkey でも同じに動くようにする。
func previousID(id string) (string, error) {
	msPart, seqPart, ok := strings.Cut(id, "-")
	if !ok || !StreamIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid stream id %q", id)
	}
	ms, err1 := strconv.ParseUint(msPart, 10, 64)
	seq, err2 := strconv.ParseUint(seqPart, 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return "", fmt.Errorf("invalid stream id %q: %w", id, err)
	}
	switch {
	case seq > 0:
		return fmt.Sprintf("%d-%d", ms, seq-1), nil
	case ms > 0:
		return fmt.Sprintf("%d-%d", ms-1, uint64(math.MaxUint64)), nil
	default:
		return "", nil
	}
}

func entryFromMessage(m redis.XMessage) Entry {
	str := func(k string) string {
		v, _ := m.Values[k].(string)
		return v
	}
	e := Entry{
		ID:         m.ID,
		Operator:   str("operator"),
		MgmtClient: str("mgmt_client"),
		Action:     str("action"),
		Target:     str("target"),
		Detail:     str("detail"),
		TraceID:    str("trace_id"),
	}
	if msPart, _, ok := strings.Cut(m.ID, "-"); ok {
		if ms, err := strconv.ParseInt(msPart, 10, 64); err == nil {
			e.Time = time.UnixMilli(ms).UTC()
		}
	}
	return e
}
