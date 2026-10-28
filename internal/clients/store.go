// Package clients は RADIUS クライアント（エッジのトンネルの IP ごとの共有シークレットと「刻印を信頼する」の印）を
// Valkey に読み書きする（MVP の設計概要 §6.1・§12。段階 2-5 の K2）。
//
// 書き込みはコマンド（aka-radius client …）だけで、管理API は読み出しだけ（GET /clients。共有シークレットは返さない）。
// auth・acct はパケットのたびに client:{ip} を引く（internal/radiussrv）ので、変更はすぐに効く。
package clients

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// KeyPrefix は RADIUS クライアントのキー（Hash）の接頭辞。client:{ip}
	KeyPrefix = "client:"
	// IndexKey は RADIUS クライアントの IP の索引（Set。一覧と数）。
	IndexKey = "clients"
)

// 共有シークレットの長さ（RFC 2865 §3 は 16 オクテット以上を勧める）
const (
	MinSecretLen = 16
	MaxSecretLen = 128
)

var (
	// ErrExists は、同じ IP の RADIUS クライアントがすでにあること。
	ErrExists = errors.New("radius client already exists")
	// ErrNotFound は、RADIUS クライアントがないこと。
	ErrNotFound = errors.New("radius client not found")

	namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// secretPattern は共有シークレットに使える文字（空白と制御文字を除く ASCII）。
	secretPattern = regexp.MustCompile(`^[\x21-\x7E]+$`)
)

// Client は RADIUS クライアント。
type Client struct {
	IP     string
	Name   string
	Secret string
	// TrustStamp は、このクライアントが付けたテナントの刻印を信頼するか（エッジは true。MVP の設計概要 §8.3）。
	TrustStamp bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NormalizeIP は IP のリテラルを、auth・acct が送信元を引くときと同じ形（IPv4 は 4 つの数、IPv6 は短い形）に直す。
// ゾーンつきの IPv6 と、IP でないものは誤りにする。
func NormalizeIP(s string) (string, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Zone() != "" {
		return "", fmt.Errorf("ip must be an IPv4 or IPv6 address without a zone (got %q)", s)
	}
	return addr.Unmap().String(), nil
}

// CheckName は名前の形（英数字と . _ -、1〜64 文字）を確かめる。
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("name must be 1-64 characters of A-Z, a-z, 0-9, '.', '_' and '-'")
	}
	return nil
}

// CheckSecret は共有シークレットの形（空白と制御文字を除く ASCII で 16〜128 文字）を確かめる。
func CheckSecret(secret string) error {
	if len(secret) < MinSecretLen || len(secret) > MaxSecretLen || !secretPattern.MatchString(secret) {
		return fmt.Errorf("secret must be %d-%d printable ASCII characters without spaces", MinSecretLen, MaxSecretLen)
	}
	return nil
}

// GenerateSecret は 32 文字のランダムな共有シークレット（24 バイトの乱数の base64url）を作る。
func GenerateSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b) // crypto/rand.Read は失敗しない
	return base64.RawURLEncoding.EncodeToString(b)
}

// Store は RADIUS クライアントを Valkey に読み書きする。
type Store struct {
	rdb *redis.Client
	now func() time.Time
}

// NewStore は Store を作る。
func NewStore(rdb *redis.Client) *Store {
	return &Store{rdb: rdb, now: time.Now}
}

func (s *Store) timestamp() string {
	return s.now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func trustValue(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// addScript は、同じ IP がなければ client:{ip} を書き、索引に足す。あれば 0 を返す。
var addScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'name', ARGV[2], 'secret', ARGV[3], 'trust_stamp', ARGV[4], 'created_at', ARGV[5], 'updated_at', ARGV[5])
redis.call('SADD', KEYS[2], ARGV[1])
return 1`)

// Add は RADIUS クライアントを登録する。c.IP は NormalizeIP で直したものを渡す。
func (s *Store) Add(ctx context.Context, c Client) error {
	n, err := addScript.Run(ctx, s.rdb, []string{KeyPrefix + c.IP, IndexKey},
		c.IP, c.Name, c.Secret, trustValue(c.TrustStamp), s.timestamp()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrExists
	}
	return nil
}

// Update は変える項目。nil の項目は変えない。
type Update struct {
	Name       *string
	Secret     *string
	TrustStamp *bool
}

// updateScript は、client:{ip} があれば項目を書き換える。なければ 0 を返す。
var updateScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV))
return 1`)

// Update は RADIUS クライアントの項目を変える。
func (s *Store) Update(ctx context.Context, ip string, u Update) error {
	args := []any{"updated_at", s.timestamp()}
	if u.Name != nil {
		args = append(args, "name", *u.Name)
	}
	if u.Secret != nil {
		args = append(args, "secret", *u.Secret)
	}
	if u.TrustStamp != nil {
		args = append(args, "trust_stamp", trustValue(*u.TrustStamp))
	}
	n, err := updateScript.Run(ctx, s.rdb, []string{KeyPrefix + ip}, args...).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// removeScript は client:{ip} を消し、索引から外す。消したキーの数を返す。
var removeScript = redis.NewScript(`
redis.call('SREM', KEYS[2], ARGV[1])
return redis.call('DEL', KEYS[1])`)

// Remove は RADIUS クライアントを消す。
func (s *Store) Remove(ctx context.Context, ip string) error {
	n, err := removeScript.Run(ctx, s.rdb, []string{KeyPrefix + ip, IndexKey}, ip).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get は RADIUS クライアントを 1 つ返す。
func (s *Store) Get(ctx context.Context, ip string) (Client, error) {
	f, err := s.rdb.HGetAll(ctx, KeyPrefix+ip).Result()
	if err != nil {
		return Client{}, err
	}
	if len(f) == 0 {
		return Client{}, ErrNotFound
	}
	return fromFields(ip, f), nil
}

// List は索引にある RADIUS クライアントを IP の順に返す（エッジの数だけなので、ページ送りはしない）。
func (s *Store) List(ctx context.Context) ([]Client, error) {
	ips, err := s.rdb.SMembers(ctx, IndexKey).Result()
	if err != nil {
		return nil, err
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ips))
	for i, ip := range ips {
		cmds[i] = pipe.HGetAll(ctx, KeyPrefix+ip)
	}
	if len(ips) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]Client, 0, len(ips))
	for i, ip := range ips {
		if f := cmds[i].Val(); len(f) > 0 {
			out = append(out, fromFields(ip, f))
		}
	}
	slices.SortFunc(out, func(a, b Client) int {
		x, errX := netip.ParseAddr(a.IP)
		y, errY := netip.ParseAddr(b.IP)
		if errX != nil || errY != nil {
			return cmp.Compare(a.IP, b.IP)
		}
		return x.Compare(y)
	})
	return out, nil
}

// Count は索引にある RADIUS クライアントの数を返す（/status）。
func (s *Store) Count(ctx context.Context) (int64, error) {
	return s.rdb.SCard(ctx, IndexKey).Result()
}

func fromFields(ip string, f map[string]string) Client {
	c := Client{IP: ip, Name: f["name"], Secret: f["secret"], TrustStamp: f["trust_stamp"] == "1"}
	c.CreatedAt, _ = time.Parse(time.RFC3339, f["created_at"])
	c.UpdatedAt, _ = time.Parse(time.RFC3339, f["updated_at"])
	return c
}
