// Package usage は利用の集計（IMSI・テナント・日ごと。課金レコード §5.2、MVP の設計概要 §10.2）の
// Valkey のキーと、認証の成功の足し込み、取り出しを提供する。Accounting の足し込みは acct の Lua が行う。
package usage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Valkey のキー（MVP の設計概要 §6.1）。
const (
	// KeyPrefix は集計（Hash）のキーの接頭辞。キーは usage:{yyyymmdd}:{tenantId}:{imsi}
	KeyPrefix = "usage:"
	// KeyPrefixIndex はその日の集計の索引（Sorted Set。メンバーは {tenantId}:{imsi}、スコアは 0）の接頭辞
	KeyPrefixIndex = "usage:idx:"
)

// 集計の項目（Hash のフィールド）。
const (
	FieldAuthOK    = "auth_ok"
	FieldSessions  = "sessions"
	FieldSeconds   = "seconds"
	FieldInOctets  = "in_octets"
	FieldOutOctets = "out_octets"
)

// RetentionDays は集計を取り出せる日数。キーは、その日の始まりから 8 日（1 日の余裕）で消える。
const RetentionDays = 7

// NoTenant は刻印のないパケットのテナントの ID（MVP の設計概要 §10.2）。
const NoTenant int64 = 0

// JST は日の区切りに使う日本時間（+09:00 の固定）。
var JST = time.FixedZone("JST", 9*60*60)

// Day は t の日本時間の日付（yyyymmdd）を返す。
func Day(t time.Time) string { return t.In(JST).Format("20060102") }

// Key は集計のキーを返す。
func Key(day string, tenantID int64, imsi string) string {
	return KeyPrefix + day + ":" + Member(tenantID, imsi)
}

// IndexKey はその日の索引のキーを返す。
func IndexKey(day string) string { return KeyPrefixIndex + day }

// Member は索引のメンバー（{tenantId}:{imsi}）を返す。
func Member(tenantID int64, imsi string) string {
	return strconv.FormatInt(tenantID, 10) + ":" + imsi
}

// ExpireAt は day の集計のキーが消える時刻（その日の始まりから RetentionDays+1 日）を返す。
func ExpireAt(day string) (time.Time, error) {
	t, err := time.ParseInLocation("20060102", day, JST)
	if err != nil {
		return time.Time{}, err
	}
	return t.AddDate(0, 0, RetentionDays+1), nil
}

// Placement は Accounting を足し込む日の決め方の結果。
type Placement struct {
	// Day は足し込む日（yyyymmdd）。TooOld なら空
	Day string
	// TooOld は、集計の期限（RetentionDays）より前の日なので足し込まないこと
	TooOld bool
	// Future は、Event-Timestamp が受け取った時刻より 1 時間以上先なので、受け取った時刻を使ったこと
	Future bool
}

// futureSlack は Event-Timestamp を未来とみなす幅。
const futureSlack = time.Hour

// Place は、Event-Timestamp（なければ空の時刻）と受け取った時刻から、足し込む日を決める（MVP の設計概要 §10.2。段階 2-4 の J5）。
func Place(event, now time.Time) Placement {
	t := now
	var p Placement
	if !event.IsZero() {
		if event.After(now.Add(futureSlack)) {
			p.Future = true
		} else {
			t = event
		}
	}
	oldest, _ := time.ParseInLocation("20060102", Day(now), JST)
	oldest = oldest.AddDate(0, 0, -RetentionDays)
	if t.Before(oldest) {
		p.TooOld = true
		return p
	}
	p.Day = Day(t)
	return p
}

// Store は集計の読み書き。
type Store struct {
	rdb redis.Cmdable
}

// NewStore は Store を作る。
func NewStore(rdb redis.Cmdable) *Store { return &Store{rdb: rdb} }

// AddAuthOK は認証の成功を 1 足す（auth の Access-Accept。日は受け取った時刻の日本時間。段階 2-4 の J7）。
func (s *Store) AddAuthOK(ctx context.Context, now time.Time, tenantID int64, imsi string) error {
	day := Day(now)
	exp, _ := ExpireAt(day)
	key, idx := Key(day, tenantID, imsi), IndexKey(day)
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HIncrBy(ctx, key, FieldAuthOK, 1)
		p.ExpireAt(ctx, key, exp)
		p.ZAdd(ctx, idx, redis.Z{Score: 0, Member: Member(tenantID, imsi)})
		p.ExpireAt(ctx, idx, exp)
		return nil
	})
	if err != nil {
		return fmt.Errorf("add auth_ok: %w", err)
	}
	return nil
}

// Record はある日の、あるテナントの、ある IMSI の集計。
type Record struct {
	Day       string
	TenantID  int64
	IMSI      string
	AuthOK    int64
	Sessions  int64
	Seconds   int64
	InOctets  int64
	OutOctets int64
}

// List は day の集計を、索引のメンバーの辞書順に limit 件まで返す。cursor は前のページの next。
func (s *Store) List(ctx context.Context, day, cursor string, limit int) (records []Record, next string, err error) {
	start := "-"
	if cursor != "" {
		start = "(" + cursor
	}
	members, err := s.rdb.ZRangeByLex(ctx, IndexKey(day), &redis.ZRangeBy{Min: start, Max: "+", Count: int64(limit) + 1}).Result()
	if err != nil {
		return nil, "", fmt.Errorf("list usage: %w", err)
	}
	if len(members) > limit {
		members = members[:limit]
		next = members[limit-1]
	}
	cmds := make([]*redis.MapStringStringCmd, len(members))
	if _, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, m := range members {
			cmds[i] = p.HGetAll(ctx, KeyPrefix+day+":"+m)
		}
		return nil
	}); err != nil {
		return nil, "", fmt.Errorf("list usage: %w", err)
	}
	records = make([]Record, 0, len(members))
	for i, m := range members {
		tenant, imsi, ok := strings.Cut(m, ":")
		tid, err := strconv.ParseInt(tenant, 10, 64)
		if !ok || err != nil {
			continue
		}
		f := cmds[i].Val()
		records = append(records, Record{
			Day: day, TenantID: tid, IMSI: imsi,
			AuthOK: num(f[FieldAuthOK]), Sessions: num(f[FieldSessions]), Seconds: num(f[FieldSeconds]),
			InOctets: num(f[FieldInOctets]), OutOctets: num(f[FieldOutOctets]),
		})
	}
	return records, next, nil
}

func num(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
