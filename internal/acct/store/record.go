package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/usage"
	"github.com/redis/go-redis/v9"
)

// KeyPrefixSeen は Accounting の重複の判定と前回の累計（Hash）のキーの接頭辞
// （acct:seen:{送信元の IP}:{NAS-Identifier か NAS-IP-Address}:{Acct-Session-Id}。段階 2-4 の J2・J3）。
const KeyPrefixSeen = "acct:seen:"

// SeenTTL は重複の判定と前回の累計を持つ時間（受けるたびに延ばす）。
const SeenTTL = 24 * time.Hour

// SeenKey は重複の判定のキーを返す。nas は NAS-Identifier（なければ NAS-IP-Address）。
func SeenKey(srcIP, nas, acctSessionID string) string {
	return KeyPrefixSeen + srcIP + ":" + nas + ":" + acctSessionID
}

// Kind は Accounting の種類。
type Kind string

const (
	KindStart   Kind = "start"
	KindInterim Kind = "interim"
	KindStop    Kind = "stop"
)

// Record は 1 つの Accounting-Request（Start・Interim・Stop）。
type Record struct {
	Kind Kind
	// SeenKey は重複の判定のキー（SeenKey で作る）
	SeenKey string
	// SessionID は Class のセッションの ID（なければ空）
	SessionID string
	// Seconds・InOctets・OutOctets は Accounting の累計（Gigawords を含む）
	Seconds   uint64
	InOctets  uint64
	OutOctets uint64
	// StampTenant は信頼する送信元の刻印のテナント（なければ 0）
	StampTenant int64
	// UserNameIMSI は User-Name の永続 ID の IMSI（なければ空）
	UserNameIMSI string
	// Day は足し込む日（yyyymmdd。空なら集計に足し込まない）
	Day string
	Now time.Time
	// セッションに残す項目
	NasIP            string
	NasIdentifier    string
	CallingStationID string
	FramedIP         string
	AcctSessionID    string
}

// Result は Account の結果。
type Result struct {
	// Duplicate は重複（Start の重複、Stop の後の Stop、同じ値の Interim）
	Duplicate bool
	// Sequence は順序の誤りの理由（start_after_stop・no_start_received・interim_after_stop。なければ空）
	Sequence string
	// IMSI と TenantID は集計に使った値（IMSI が空なら集計していない）
	IMSI     string
	TenantID int64
	// TenantSource はテナントの出どころ（stamp・seen・session・none）
	TenantSource string
	// Seconds・InOctets・OutOctets は足し込んだ増分
	Seconds   int64
	InOctets  int64
	OutOctets int64
	// CounterReset は累計が減った（NAS の作り直しなど）ので、その値をそのまま増分にしたこと
	CounterReset bool
	// SessionFound は Class のセッションがあったこと
	SessionFound bool
	// UsageWritten は集計に足し込んだこと
	UsageWritten bool
}

// accountScript は、重複の判定、前回の累計からの増分、累計の更新、利用の集計への足し込み、セッションと索引の更新を
// 1 つで行う（MVP の設計概要 §10。段階 2-4 の J2・J4・J6・J8）。Lua の中でキーを組み立てるので、Valkey は単一ノードで使う。
//
// KEYS[1] 重複の判定のキー、KEYS[2] セッションのキー（ARGV[17] が 1 のときだけ使う）
// ARGV: 1 種類、2〜4 累計（時間・受信・送信）、5 刻印のテナント（なければ空）、6 User-Name の IMSI、7 今（Unix 秒）、
// 8 セッションの期限（Unix 秒）、9 日（空なら足し込まない）、10 集計のキーの期限（Unix 秒）、11 NAS-IP、12 NAS-Identifier、
// 13 Calling-Station-Id、14 Framed-IP、15 Acct-Session-Id、16 重複の判定の期限（秒）、17 セッションがあるか、
// 18 集計のキーの接頭辞、19 集計の索引の接頭辞、20 セッションの IMSI の索引の接頭辞、21 テナントの索引の接頭辞、22 セッションの ID
//
// 戻り値: {重複, 順序の誤り, IMSI, テナント, 時間の増分, 受信の増分, 送信の増分, 累計の減少, テナントの出どころ, セッションあり, 集計あり}
var accountScript = redis.NewScript(`
local function nz(v) if v and v ~= '' then return v end return nil end
local typ = ARGV[1]
local cur = {tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4])}
local seen = redis.call('HMGET', KEYS[1], 'last', 'sec', 'in', 'out', 'imsi', 'tenant')
local last = seen[1] or ''
local prev = {tonumber(seen[2]) or 0, tonumber(seen[3]) or 0, tonumber(seen[4]) or 0}
local dup, seq = 0, ''
if typ == 'start' then
  if last == 'start' or last == 'interim' then return {1, '', '', '', 0, 0, 0, 0, '', 0, 0} end
  if last == 'stop' then seq = 'start_after_stop' end
  prev = {0, 0, 0}
elseif typ == 'interim' then
  if last == '' then seq = 'no_start_received' elseif last == 'stop' then seq = 'interim_after_stop' end
  if last == 'interim' and cur[1] == prev[1] and cur[2] == prev[2] and cur[3] == prev[3] then dup = 1 end
else
  if last == 'stop' then return {1, '', '', '', 0, 0, 0, 0, '', 0, 0} end
  if last == '' then seq = 'no_start_received' end
end

local hasSess, sImsi, sTenant = 0, nil, nil
if ARGV[17] == '1' then
  local sf = redis.call('HMGET', KEYS[2], 'imsi', 'tenant_id')
  if sf[1] then hasSess, sImsi, sTenant = 1, nz(sf[1]), nz(sf[2]) end
end
local imsi = sImsi or nz(seen[5]) or nz(ARGV[6]) or ''
local tenant, tsrc = '0', 'none'
if nz(ARGV[5]) then tenant, tsrc = ARGV[5], 'stamp'
elseif nz(seen[6]) and seen[6] ~= '0' then tenant, tsrc = seen[6], 'seen'
elseif sTenant and sTenant ~= '0' then tenant, tsrc = sTenant, 'session' end

local d, reset = {0, 0, 0}, 0
for i = 1, 3 do
  d[i] = cur[i] - prev[i]
  if d[i] < 0 then d[i] = cur[i]; reset = 1 end
end

local written = 0
if imsi ~= '' and ARGV[9] ~= '' and dup == 0 then
  local member = tenant .. ':' .. imsi
  local ukey = ARGV[18] .. ARGV[9] .. ':' .. member
  if typ == 'start' then redis.call('HINCRBY', ukey, 'sessions', 1); written = 1 end
  local fields = {'seconds', 'in_octets', 'out_octets'}
  for i = 1, 3 do
    if d[i] > 0 then redis.call('HINCRBY', ukey, fields[i], string.format('%d', d[i])); written = 1 end
  end
  if written == 1 then
    local idx = ARGV[19] .. ARGV[9]
    redis.call('EXPIREAT', ukey, ARGV[10])
    redis.call('ZADD', idx, 0, member)
    redis.call('EXPIREAT', idx, ARGV[10])
  end
end

redis.call('HSET', KEYS[1], 'last', typ, 'sec', string.format('%d', cur[1]), 'in', string.format('%d', cur[2]),
  'out', string.format('%d', cur[3]), 'imsi', imsi, 'tenant', tenant)
redis.call('EXPIRE', KEYS[1], ARGV[16])

if hasSess == 1 then
  local id = ARGV[22]
  if typ == 'stop' then
    redis.call('DEL', KEYS[2])
    if sImsi then redis.call('ZREM', ARGV[20] .. sImsi, id) end
    if sTenant then redis.call('ZREM', ARGV[21] .. sTenant, id) end
  else
    redis.call('HSET', KEYS[2], 'nas_ip', ARGV[11], 'nas_identifier', ARGV[12], 'calling_station_id', ARGV[13],
      'client_ip', ARGV[14], 'acct_id', ARGV[15], 'last_update', ARGV[7], 'seconds', string.format('%d', cur[1]),
      'input_octets', string.format('%d', cur[2]), 'output_octets', string.format('%d', cur[3]))
    if typ == 'start' then redis.call('HSET', KEYS[2], 'start_time', ARGV[7]) end
    redis.call('EXPIREAT', KEYS[2], ARGV[8])
    if sImsi then redis.call('ZADD', ARGV[20] .. sImsi, ARGV[8], id) end
    if sTenant then redis.call('ZADD', ARGV[21] .. sTenant, ARGV[8], id) end
  end
end
return {dup, seq, imsi, tenant, d[1], d[2], d[3], reset, tsrc, hasSess, written}
`)

// RecordStore は Accounting の記録。
type RecordStore struct {
	rdb redis.Cmdable
}

// NewRecordStore は RecordStore を作る。
func NewRecordStore(rdb redis.Cmdable) *RecordStore { return &RecordStore{rdb: rdb} }

// Account は 1 つの Accounting-Request を記録する。
func (s *RecordStore) Account(ctx context.Context, r Record) (Result, error) {
	stamp := ""
	if r.StampTenant > 0 {
		stamp = strconv.FormatInt(r.StampTenant, 10)
	}
	hasSess, sessKey := "0", sessions.KeyPrefix
	if r.SessionID != "" {
		hasSess, sessKey = "1", sessions.Key(r.SessionID)
	}
	var usageExp int64
	if r.Day != "" {
		exp, err := usage.ExpireAt(r.Day)
		if err != nil {
			return Result{}, fmt.Errorf("account: %w", err)
		}
		usageExp = exp.Unix()
	}
	res, err := accountScript.Run(ctx, s.rdb, []string{r.SeenKey, sessKey},
		string(r.Kind), r.Seconds, r.InOctets, r.OutOctets, stamp, r.UserNameIMSI,
		r.Now.Unix(), r.Now.Add(sessions.TTL).Unix(), r.Day, usageExp,
		r.NasIP, r.NasIdentifier, r.CallingStationID, r.FramedIP, r.AcctSessionID,
		int64(SeenTTL/time.Second), hasSess,
		usage.KeyPrefix, usage.KeyPrefixIndex, sessions.KeyPrefixIMSIIndex, sessions.KeyPrefixTenantIndex, r.SessionID,
	).Slice()
	if err != nil {
		return Result{}, fmt.Errorf("account: %w", err)
	}
	if len(res) != 11 {
		return Result{}, fmt.Errorf("account: unexpected result %v", res)
	}
	i := func(n int) int64 { v, _ := res[n].(int64); return v }
	str := func(n int) string { v, _ := res[n].(string); return v }
	tenant, _ := strconv.ParseInt(str(3), 10, 64)
	return Result{
		Duplicate:    i(0) == 1,
		Sequence:     str(1),
		IMSI:         str(2),
		TenantID:     tenant,
		Seconds:      i(4),
		InOctets:     i(5),
		OutOctets:    i(6),
		CounterReset: i(7) == 1,
		TenantSource: str(8),
		SessionFound: i(9) == 1,
		UsageWritten: i(10) == 1,
	}, nil
}
