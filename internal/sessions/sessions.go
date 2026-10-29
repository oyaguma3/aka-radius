// Package sessions はセッション（sess:{uuid}）と、その索引（IMSI ごと・テナントごと）の Valkey のキーと読み書きを提供する
// （MVP の設計概要 §10.1。決定事項 12）。auth が Access-Accept で作り、acct が Accounting で更新・削除し、admin が読む。
package sessions

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Valkey のキー（MVP の設計概要 §6.1）。
const (
	// KeyPrefix はセッション（Hash）のキーの接頭辞
	KeyPrefix = "sess:"
	// KeyPrefixIMSIIndex・KeyPrefixTenantIndex は索引（Sorted Set。メンバーはセッションの ID、スコアは期限の時刻（Unix 秒））
	KeyPrefixIMSIIndex   = "idx:sess:imsi:"
	KeyPrefixTenantIndex = "idx:sess:tenant:"
	// KeyStats は、定期の掃除で数えたセッションの数と数えた時刻（Hash）
	KeyStats = "stat:sessions"
)

// TTL はセッションの期限（Accounting-Start・Interim で延ばす。Stop で消す）。
const TTL = 24 * time.Hour

// Key はセッションのキーを返す。
func Key(id string) string { return KeyPrefix + id }

// IMSIIndexKey は IMSI の索引のキーを返す。
func IMSIIndexKey(imsi string) string { return KeyPrefixIMSIIndex + imsi }

// TenantIndexKey はテナントの索引のキーを返す。
func TenantIndexKey(tenantID int64) string {
	return KeyPrefixTenantIndex + strconv.FormatInt(tenantID, 10)
}

// Session はセッション。
type Session struct {
	ID       string
	IMSI     string
	TenantID int64
	// NasIP は送信元（エッジのトンネルの IP）、NasIdentifier は AP の NAS-Identifier
	NasIP            string
	NasIdentifier    string
	CallingStationID string
	FramedIP         string
	AcctSessionID    string
	StartTime        time.Time
	LastUpdate       time.Time
	// Seconds・InOctets・OutOctets は Accounting の最後の累計（Gigawords を含む）
	Seconds   int64
	InOctets  int64
	OutOctets int64
	// ExpiresAt は索引のスコア（期限の時刻）
	ExpiresAt time.Time
}

// Store はセッションの読み書き。
type Store struct {
	rdb redis.Cmdable
}

// NewStore は Store を作る。
func NewStore(rdb redis.Cmdable) *Store { return &Store{rdb: rdb} }

// Create はセッションを作り、IMSI とテナントの索引に足す（auth の Access-Accept）。
func (s *Store) Create(ctx context.Context, sess Session, now time.Time) error {
	exp := now.Add(TTL)
	key := Key(sess.ID)
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key,
			"imsi", sess.IMSI,
			"tenant_id", sess.TenantID,
			"nas_ip", sess.NasIP,
			"nas_identifier", sess.NasIdentifier,
			"start_time", now.Unix(),
			"last_update", now.Unix(),
		)
		p.ExpireAt(ctx, key, exp)
		z := redis.Z{Score: float64(exp.Unix()), Member: sess.ID}
		p.ZAdd(ctx, IMSIIndexKey(sess.IMSI), z)
		p.ZAdd(ctx, TenantIndexKey(sess.TenantID), z)
		return nil
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// ListByTenant はテナントの索引のセッションを、期限の早い順に offset から limit 件まで返す。
// 期限を過ぎた索引は読む前に消す。続きがあれば next に次の offset を返す（なければ -1）。
func (s *Store) ListByTenant(ctx context.Context, tenantID int64, offset, limit int, now time.Time) ([]Session, int, error) {
	key := TenantIndexKey(tenantID)
	if err := s.trim(ctx, key, now); err != nil {
		return nil, -1, err
	}
	zs, err := s.rdb.ZRangeArgsWithScores(ctx, redis.ZRangeArgs{
		Key: key, Start: "-inf", Stop: "+inf", ByScore: true, Offset: int64(offset), Count: int64(limit) + 1,
	}).Result()
	if err != nil {
		return nil, -1, fmt.Errorf("list sessions: %w", err)
	}
	next := -1
	if len(zs) > limit {
		zs = zs[:limit]
		next = offset + limit
	}
	out, err := s.load(ctx, zs)
	return out, next, err
}

// ListByIMSIs は IMSI の索引のセッションを、期限の早い順に offset から limit 件まで返す。
func (s *Store) ListByIMSIs(ctx context.Context, imsis []string, offset, limit int, now time.Time) ([]Session, int, error) {
	var all []redis.Z
	for _, imsi := range imsis {
		key := IMSIIndexKey(imsi)
		if err := s.trim(ctx, key, now); err != nil {
			return nil, -1, err
		}
		zs, err := s.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			return nil, -1, fmt.Errorf("list sessions: %w", err)
		}
		all = append(all, zs...)
	}
	slices.SortStableFunc(all, func(a, b redis.Z) int {
		switch {
		case a.Score < b.Score:
			return -1
		case a.Score > b.Score:
			return 1
		}
		return 0
	})
	next := -1
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if len(all) > limit {
		all = all[:limit]
		next = offset + limit
	}
	out, err := s.load(ctx, all)
	return out, next, err
}

// trim は索引から期限を過ぎたものを消す。
func (s *Store) trim(ctx context.Context, key string, now time.Time) error {
	if err := s.rdb.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatInt(now.Unix(), 10)).Err(); err != nil {
		return fmt.Errorf("trim session index: %w", err)
	}
	return nil
}

// load は索引のメンバーのセッションを読む。索引にあってセッションが消えているものは飛ばす。
func (s *Store) load(ctx context.Context, zs []redis.Z) ([]Session, error) {
	cmds := make([]*redis.MapStringStringCmd, len(zs))
	if _, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, z := range zs {
			cmds[i] = p.HGetAll(ctx, Key(z.Member.(string)))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	out := make([]Session, 0, len(zs))
	for i, z := range zs {
		f := cmds[i].Val()
		if len(f) == 0 {
			continue
		}
		out = append(out, fromHash(z.Member.(string), f, int64(z.Score)))
	}
	return out, nil
}

func fromHash(id string, f map[string]string, exp int64) Session {
	return Session{
		ID:               id,
		IMSI:             f["imsi"],
		TenantID:         num(f["tenant_id"]),
		NasIP:            f["nas_ip"],
		NasIdentifier:    f["nas_identifier"],
		CallingStationID: f["calling_station_id"],
		FramedIP:         f["client_ip"],
		AcctSessionID:    f["acct_id"],
		StartTime:        unix(f["start_time"]),
		LastUpdate:       unix(f["last_update"]),
		Seconds:          num(f["seconds"]),
		InOctets:         num(f["input_octets"]),
		OutOctets:        num(f["output_octets"]),
		ExpiresAt:        time.Unix(exp, 0).UTC(),
	}
}

func num(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func unix(s string) time.Time {
	n := num(s)
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// Sweep は、すべての索引から期限を過ぎたものを消し、テナントの索引のセッションの数を数えて KeyStats に残す
// （acct が 1 時間ごとに行う。段階 2-4 の J8・J11）。数えたセッションの数を返す。
func (s *Store) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var count int64
	for _, prefix := range []string{KeyPrefixIMSIIndex, KeyPrefixTenantIndex} {
		iter := s.rdb.Scan(ctx, 0, prefix+"*", 1000).Iterator()
		for iter.Next(ctx) {
			key := iter.Val()
			if err := s.trim(ctx, key, now); err != nil {
				return 0, err
			}
			if prefix == KeyPrefixTenantIndex {
				n, err := s.rdb.ZCard(ctx, key).Result()
				if err != nil {
					return 0, fmt.Errorf("count sessions: %w", err)
				}
				count += n
			}
		}
		if err := iter.Err(); err != nil {
			return 0, fmt.Errorf("scan session indexes: %w", err)
		}
	}
	if err := s.rdb.HSet(ctx, KeyStats, "count", count, "counted_at", now.Unix()).Err(); err != nil {
		return 0, fmt.Errorf("write session stats: %w", err)
	}
	return count, nil
}

// Stats は、定期の掃除で数えたセッションの数と、数えた時刻を返す（まだ数えていなければ ok が false）。
func (s *Store) Stats(ctx context.Context) (count int64, countedAt time.Time, ok bool, err error) {
	f, err := s.rdb.HGetAll(ctx, KeyStats).Result()
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("read session stats: %w", err)
	}
	if f["counted_at"] == "" {
		return 0, time.Time{}, false, nil
	}
	return num(f["count"]), unix(f["counted_at"]), true, nil
}
