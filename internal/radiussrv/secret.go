package radiussrv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/oyaguma3/aka-radius/internal/clients"
	"github.com/redis/go-redis/v9"
)

// keyPrefixClient はRADIUSクライアント設定のキーの接頭辞（書き込みはコマンド aka-radius client。internal/clients）
const keyPrefixClient = clients.KeyPrefix

// ClientStore はRADIUSクライアントの共有シークレットの取得を定義する
type ClientStore interface {
	// GetClientSecret は指定されたIPのShared Secretを取得する
	// 未登録の場合は空文字列とnilを返す
	GetClientSecret(ctx context.Context, ip string) (string, error)
}

// valkeyClientStore はValkeyの client:{IP} から共有シークレットを引くClientStoreの実装。
type valkeyClientStore struct {
	rdb *redis.Client
}

// NewClientStore はValkeyを引くClientStoreを生成する。
func NewClientStore(rdb *redis.Client) ClientStore {
	return &valkeyClientStore{rdb: rdb}
}

// GetClientSecret は指定されたIPのShared Secretを取得する。
// 未登録の場合は空文字列とnilを返す。
func (s *valkeyClientStore) GetClientSecret(ctx context.Context, ip string) (string, error) {
	secret, err := s.rdb.HGet(ctx, keyPrefixClient+ip, "secret").Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		}
		return "", fmt.Errorf("valkey unavailable: %w", err)
	}
	return secret, nil
}

// StampTrust は、RADIUS クライアントの「刻印を信頼する」の印（client:{IP} の trust_stamp が 1）を引く（MVP の設計概要 §8.3）。
type StampTrust struct {
	rdb *redis.Client
}

// NewStampTrust は StampTrust を作る。
func NewStampTrust(rdb *redis.Client) *StampTrust {
	return &StampTrust{rdb: rdb}
}

// TrustsStamp は、送信元の RADIUS クライアントの刻印を使ってよいかを返す。
// 登録のない送信元（予備の共有シークレットで受けたもの）と、印のない RADIUS クライアントは false。
func (s *StampTrust) TrustsStamp(ctx context.Context, ip string) (bool, error) {
	v, err := s.rdb.HGet(ctx, keyPrefixClient+ip, "trust_stamp").Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("valkey unavailable: %w", err)
	}
	return v == "1", nil
}

// DynamicSecretSource はValkey登録情報に基づくRADIUS Secret解決を行う。
// layeh.com/radius.SecretSourceインターフェースの実装。
type DynamicSecretSource struct {
	clientStore    ClientStore
	fallbackSecret []byte
}

// NewSecretSource は新しいDynamicSecretSourceを生成する。
// fallbackSecretが空文字列の場合、フォールバックは無効。
func NewSecretSource(cs ClientStore, fallbackSecret string) *DynamicSecretSource {
	var fb []byte
	if fallbackSecret != "" {
		fb = []byte(fallbackSecret)
	}
	return &DynamicSecretSource{
		clientStore:    cs,
		fallbackSecret: fb,
	}
}

// RADIUSSecret はリモートアドレスに対応するRADIUS Secretを返す。
// Valkey登録 → フォールバック → nilの優先順で解決する。
func (s *DynamicSecretSource) RADIUSSecret(ctx context.Context, remoteAddr net.Addr) ([]byte, error) {
	ip := ExtractIP(remoteAddr)
	if ip == "" {
		var addrStr string
		if remoteAddr != nil {
			addrStr = remoteAddr.String()
		}
		slog.Warn("IPアドレス抽出失敗",
			"event_id", "RADIUS_IP_EXTRACT_ERR",
			"remote_addr", addrStr,
		)
		if len(s.fallbackSecret) > 0 {
			return s.fallbackSecret, nil
		}
		return nil, nil
	}

	secret, err := s.clientStore.GetClientSecret(ctx, ip)
	if err != nil {
		slog.Warn("Valkeyクライアント検索エラー",
			"event_id", "RADIUS_SECRET_ERR",
			"src_ip", ip,
			"error", err,
		)
		// Valkeyエラー時はフォールバック
		if len(s.fallbackSecret) > 0 {
			return s.fallbackSecret, nil
		}
		return nil, nil
	}

	if secret != "" {
		return []byte(secret), nil
	}

	// Valkey未登録
	if len(s.fallbackSecret) > 0 {
		return s.fallbackSecret, nil
	}

	slog.Warn("RADIUS Secret不明",
		"event_id", "RADIUS_NO_SECRET",
		"src_ip", ip,
	)
	return nil, nil
}

// ExtractIP はnet.AddrからIPアドレス文字列を抽出する
func ExtractIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if udpAddr, ok := addr.(*net.UDPAddr); ok {
		return udpAddr.IP.String()
	}
	// UDPAddr以外の場合はhost部分を試行
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	return host
}
