package subscriber

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Valkey のキー（MVP の設計概要 §6.1）。
const (
	// KeyPrefix は加入者（Hash）のキーの接頭辞
	KeyPrefix = "sub:"
	// KeyIndex は IMSI の索引（Sorted Set。スコアはすべて 0 で、辞書順にたどる）
	KeyIndex = "subs"
	// KeyPrefixSourceCount は取得先ごとの加入者の数（String）のキーの接頭辞
	KeyPrefixSourceCount = "cnt:src:"
)

// Key は加入者のキーを返す。
func Key(imsi string) string { return KeyPrefix + imsi }

// SourceCountKey は取得先ごとの加入者の数のキーを返す。
func SourceCountKey(sourceID string) string { return KeyPrefixSourceCount + sourceID }

// Store は加入者の読み書き。登録・変更・状態の変更・削除は Lua で、加入者と索引と取得先ごとの数を一度に書き換える。
// Lua の中で取得先ごとの数のキーを組み立てるので、Valkey は単一ノードで使う（本PoC と同じ）。
type Store struct {
	rdb redis.Cmdable
	// now は現在時刻を返す（試験用）。
	now func() time.Time
}

// NewStore は Store を作る。
func NewStore(rdb redis.Cmdable) *Store {
	return &Store{rdb: rdb, now: time.Now}
}

func (s *Store) timestamp() string {
	return s.now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

// Get は加入者を返す。いなければ ErrNotFound、形が違えば ErrInvalid を返す。
func (s *Store) Get(ctx context.Context, imsi string) (Subscriber, error) {
	fields, err := s.rdb.HGetAll(ctx, Key(imsi)).Result()
	if err != nil {
		return Subscriber{}, fmt.Errorf("get subscriber: %w", err)
	}
	if len(fields) == 0 {
		return Subscriber{}, ErrNotFound
	}
	return fromHash(imsi, fields)
}

// GetMany は、いる加入者を返す（いない IMSI は結果に含めない）。形の違う加入者があれば ErrInvalid を返す。
func (s *Store) GetMany(ctx context.Context, imsis []string) ([]Subscriber, error) {
	if len(imsis) == 0 {
		return nil, nil
	}
	cmds := make([]*redis.MapStringStringCmd, len(imsis))
	if _, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, imsi := range imsis {
			cmds[i] = p.HGetAll(ctx, Key(imsi))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("get subscribers: %w", err)
	}
	out := make([]Subscriber, 0, len(imsis))
	for i, cmd := range cmds {
		fields := cmd.Val()
		if len(fields) == 0 {
			continue
		}
		sub, err := fromHash(imsis[i], fields)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, nil
}

func fromHash(imsi string, f map[string]string) (Subscriber, error) {
	sub := Subscriber{IMSI: imsi, State: State(f["state"]), SourceID: f["source_id"]}
	if !sub.State.Valid() || sub.SourceID == "" {
		return Subscriber{}, fmt.Errorf("%w: %s has no valid state or source_id", ErrInvalid, Key(imsi))
	}
	if err := json.Unmarshal([]byte(f["rules"]), &sub.Rules); err != nil {
		return Subscriber{}, fmt.Errorf("%w: %s rules: %v", ErrInvalid, Key(imsi), err)
	}
	sub.StateChangedAt, _ = time.Parse(time.RFC3339, f["state_changed_at"])
	sub.CreatedAt, _ = time.Parse(time.RFC3339, f["created_at"])
	sub.UpdatedAt, _ = time.Parse(time.RFC3339, f["updated_at"])
	return sub, nil
}

// createScript は加入者を登録する。既にいれば 0、登録したら 1 を返す。
var createScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'state', ARGV[2], 'state_changed_at', ARGV[5], 'source_id', ARGV[3],
  'rules', ARGV[4], 'created_at', ARGV[5], 'updated_at', ARGV[5])
redis.call('ZADD', KEYS[2], 0, ARGV[1])
redis.call('INCR', ARGV[6] .. ARGV[3])
return 1
`)

// Create は加入者を登録する。既にいれば ErrExists を返す。状態・取得先・ルールの正しさは呼び出し側で確かめる。
func (s *Store) Create(ctx context.Context, sub Subscriber) error {
	rules, err := json.Marshal(sub.Rules)
	if err != nil {
		return fmt.Errorf("create subscriber: %w", err)
	}
	n, err := createScript.Run(ctx, s.rdb, []string{Key(sub.IMSI), KeyIndex},
		sub.IMSI, string(sub.State), sub.SourceID, string(rules), s.timestamp(), KeyPrefixSourceCount).Int()
	if err != nil {
		return fmt.Errorf("create subscriber: %w", err)
	}
	if n == 0 {
		return ErrExists
	}
	return nil
}

// Patch は加入者の変更（nil の項目は変えない）。
type Patch struct {
	SourceID *string
	Rules    *[]Rule
}

// updateScript は取得先とルールを変える。いなければ 0、terminated なら -1、変えたら 1 を返す。
// 取得先を変えたら、取得先ごとの数を付け替える。
var updateScript = redis.NewScript(`
local f = redis.call('HMGET', KEYS[1], 'state', 'source_id')
if not f[1] then return 0 end
if f[1] == 'terminated' then return -1 end
if ARGV[1] ~= '' and ARGV[1] ~= f[2] then
  redis.call('HSET', KEYS[1], 'source_id', ARGV[1])
  if f[2] then redis.call('DECR', ARGV[4] .. f[2]) end
  redis.call('INCR', ARGV[4] .. ARGV[1])
end
if ARGV[2] ~= '' then redis.call('HSET', KEYS[1], 'rules', ARGV[2]) end
redis.call('HSET', KEYS[1], 'updated_at', ARGV[3])
return 1
`)

// Update は取得先とルールを変える。いなければ ErrNotFound、terminated なら ErrTerminated を返す。
func (s *Store) Update(ctx context.Context, imsi string, p Patch) error {
	var source, rules string
	if p.SourceID != nil {
		source = *p.SourceID
	}
	if p.Rules != nil {
		b, err := json.Marshal(*p.Rules)
		if err != nil {
			return fmt.Errorf("update subscriber: %w", err)
		}
		rules = string(b)
	}
	n, err := updateScript.Run(ctx, s.rdb, []string{Key(imsi)}, source, rules, s.timestamp(), KeyPrefixSourceCount).Int()
	if err != nil {
		return fmt.Errorf("update subscriber: %w", err)
	}
	switch n {
	case 0:
		return ErrNotFound
	case -1:
		return ErrTerminated
	}
	return nil
}

// stateScript は状態を変える。terminated からの変更は、この口ではなく reinstate だけで行う。
// 戻り値: {0} いない、{-1, 今の状態} 許されない、{2, 今の状態} 同じ状態、{1, 前の状態} 変えた。
var stateScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'state')
if not cur then return {0} end
if cur == ARGV[1] then return {2, cur} end
if cur == 'terminated' then return {-1, cur} end
redis.call('HSET', KEYS[1], 'state', ARGV[1], 'state_changed_at', ARGV[2], 'updated_at', ARGV[2])
return {1, cur}
`)

// SetState は状態を変え、前の状態を返す。同じ状態なら何もせず unchanged を返す。
// いなければ ErrNotFound、terminated からの変更なら ErrTransitionNotAllowed を返す。
func (s *Store) SetState(ctx context.Context, imsi string, to State) (from State, unchanged bool, err error) {
	res, err := stateScript.Run(ctx, s.rdb, []string{Key(imsi)}, string(to), s.timestamp()).Slice()
	if err != nil {
		return "", false, fmt.Errorf("set subscriber state: %w", err)
	}
	return stateResult(res)
}

// reinstateScript は terminated を inactive に戻す。戻り値は stateScript と同じ（{2} は使わない）。
var reinstateScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'state')
if not cur then return {0} end
if cur ~= 'terminated' then return {-1, cur} end
redis.call('HSET', KEYS[1], 'state', 'inactive', 'state_changed_at', ARGV[1], 'updated_at', ARGV[1])
return {1, cur}
`)

// Reinstate は terminated の加入者を inactive に戻し、前の状態を返す。
// いなければ ErrNotFound、terminated でなければ ErrTransitionNotAllowed を返す。
func (s *Store) Reinstate(ctx context.Context, imsi string) (State, error) {
	res, err := reinstateScript.Run(ctx, s.rdb, []string{Key(imsi)}, s.timestamp()).Slice()
	if err != nil {
		return "", fmt.Errorf("reinstate subscriber: %w", err)
	}
	from, _, err := stateResult(res)
	return from, err
}

func stateResult(res []any) (State, bool, error) {
	code, _ := res[0].(int64)
	var cur State
	if len(res) > 1 {
		s, _ := res[1].(string)
		cur = State(s)
	}
	switch code {
	case 0:
		return "", false, ErrNotFound
	case -1:
		return cur, false, ErrTransitionNotAllowed
	case 2:
		return cur, true, nil
	}
	return cur, false, nil
}

// deleteScript は加入者を削除する。いなければ 0、active なら -1、削除したら 1 を返す。
var deleteScript = redis.NewScript(`
local f = redis.call('HMGET', KEYS[1], 'state', 'source_id')
if not f[1] then return 0 end
if f[1] == 'active' then return -1 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
if f[2] then redis.call('DECR', ARGV[2] .. f[2]) end
return 1
`)

// Delete は加入者を物理削除する。いなければ ErrNotFound、active なら ErrActive を返す。
// セッションは消さない（期限で消える。MVP の設計概要 §11.2）。
func (s *Store) Delete(ctx context.Context, imsi string) error {
	n, err := deleteScript.Run(ctx, s.rdb, []string{Key(imsi), KeyIndex}, imsi, KeyPrefixSourceCount).Int()
	if err != nil {
		return fmt.Errorf("delete subscriber: %w", err)
	}
	switch n {
	case 0:
		return ErrNotFound
	case -1:
		return ErrActive
	}
	return nil
}

// Page は一覧の 1 ページ。
type Page struct {
	Items []Subscriber
	// Total は prefix に当たる加入者の数
	Total int64
	// NextCursor は次のページの cursor（最後のページなら空）
	NextCursor string
}

// List は IMSI の辞書順に一覧を返す。prefix は IMSI の前方一致、cursor は前のページの NextCursor。
func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (Page, error) {
	lo, hi := "-", "+"
	if prefix != "" {
		// IMSI は数字だけなので、prefix に続く文字はすべて 0xff より小さい。
		lo, hi = "["+prefix, "["+prefix+"\xff"
	}
	start := lo
	if cursor != "" {
		start = "(" + cursor
	}
	var total *redis.IntCmd
	var imsisCmd *redis.StringSliceCmd
	if _, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		total = p.ZLexCount(ctx, KeyIndex, lo, hi)
		// 次のページがあるかを知るために 1 件多く読む。
		imsisCmd = p.ZRangeByLex(ctx, KeyIndex, &redis.ZRangeBy{Min: start, Max: hi, Count: int64(limit) + 1})
		return nil
	}); err != nil {
		return Page{}, fmt.Errorf("list subscribers: %w", err)
	}
	page := Page{Total: total.Val()}
	imsis := imsisCmd.Val()
	if len(imsis) > limit {
		imsis = imsis[:limit]
		page.NextCursor = imsis[limit-1]
	}
	// 一覧の取得後に削除された加入者は、ここで結果から抜ける。
	items, err := s.GetMany(ctx, imsis)
	if err != nil {
		return Page{}, err
	}
	page.Items = items
	return page, nil
}

// Count は加入者の数を返す。
func (s *Store) Count(ctx context.Context) (int64, error) {
	n, err := s.rdb.ZCard(ctx, KeyIndex).Result()
	if err != nil {
		return 0, fmt.Errorf("count subscribers: %w", err)
	}
	return n, nil
}

// SourceCounts は取得先ごとの加入者の数を返す（ない取得先は 0）。
func (s *Store) SourceCounts(ctx context.Context, sourceIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(sourceIDs))
	if len(sourceIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(sourceIDs))
	for i, id := range sourceIDs {
		keys[i] = SourceCountKey(id)
	}
	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("count subscribers by source: %w", err)
	}
	for i, v := range vals {
		var n int64
		if str, ok := v.(string); ok {
			if n, err = strconv.ParseInt(str, 10, 64); err != nil {
				return nil, fmt.Errorf("count subscribers by source %s: %w", sourceIDs[i], err)
			}
		}
		out[sourceIDs[i]] = n
	}
	return out, nil
}
