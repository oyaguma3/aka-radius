// Package envconf は環境変数の読み込みの共通処理を提供する（標準ライブラリだけで読む）。
// 環境変数の接頭辞は RADIUS_（MVP の設計概要 §4）。
package envconf

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// String は環境変数の値を返す。空なら def を返す。
func String(name, def string) string {
	return cmp.Or(os.Getenv(name), def)
}

// Required は環境変数の値を返す。空ならエラーを返す。
func Required(name string) (string, error) {
	v := os.Getenv(name)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return v, nil
}

// Bool は環境変数を真偽値として読む（strconv.ParseBool の書き方）。空なら def を返す。
func Bool(name string, def bool) (bool, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: must be true or false", name)
	}
	return b, nil
}

// PositiveInt は環境変数を正の整数として読む。空なら def を返す。
func PositiveInt(name string, def int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: must be a positive integer", name)
	}
	return n, nil
}

// Duration は環境変数を時間（time.ParseDuration の書き方。例 4s）として読む。空なら def を返す。
// lo〜hi の外なら誤りにする。
func Duration(name string, def, lo, hi time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return 0, fmt.Errorf("%s: must be a duration between %s and %s", name, lo, hi)
	}
	return d, nil
}

// テナントの刻印の Vendor-Id と Vendor-Type の既定（概要の決定事項 29。MVP の設計概要 §8.3）
const (
	DefaultStampVendorID   = 32473
	DefaultStampVendorType = 1
)

// StampVendor は RADIUS_STAMP_VENDOR_ID（既定 32473）と RADIUS_STAMP_VENDOR_TYPE（既定 1）を読む（auth と admin で共通）。
func StampVendor() (uint32, byte, error) {
	id, err := PositiveInt("RADIUS_STAMP_VENDOR_ID", DefaultStampVendorID)
	if err != nil {
		return 0, 0, err
	}
	if id > math.MaxUint32 {
		return 0, 0, errors.New("RADIUS_STAMP_VENDOR_ID: must be at most 4294967295")
	}
	typ, err := PositiveInt("RADIUS_STAMP_VENDOR_TYPE", DefaultStampVendorType)
	if err != nil {
		return 0, 0, err
	}
	if typ > 255 {
		return 0, 0, errors.New("RADIUS_STAMP_VENDOR_TYPE: must be 1 to 255")
	}
	return uint32(id), byte(typ), nil
}

// Common は auth・acct・admin に共通する設定。
type Common struct {
	// ValkeyHost・ValkeyPort・ValkeyPassword は Valkey の接続先とパスワード。
	ValkeyHost     string
	ValkeyPort     string
	ValkeyPassword string
	// LogLevel はログの出力レベル（DEBUG / INFO / WARN / ERROR）。
	LogLevel string
	// LogMaskIMSI はログの IMSI をマスクするか（既定 false。MVP の決定事項 26）。
	LogMaskIMSI bool
	// HealthAddr は healthcheck の口（RADIUS_HEALTH_ADDR。既定 127.0.0.1:9090）。
	HealthAddr string
}

// LoadCommon は共通の設定を読む。Valkey のパスワードは必須（Valkey は requirepass で動かす）。
func LoadCommon() (Common, error) {
	c := Common{
		ValkeyHost: String("RADIUS_VALKEY_HOST", "valkey"),
		ValkeyPort: String("RADIUS_VALKEY_PORT", "6379"),
		LogLevel:   String("RADIUS_LOG_LEVEL", "INFO"),
	}
	var errs []error
	var err error
	if c.ValkeyPassword, err = Required("RADIUS_VALKEY_PASSWORD"); err != nil {
		errs = append(errs, err)
	}
	if c.LogMaskIMSI, err = Bool("RADIUS_LOG_MASK_IMSI", false); err != nil {
		errs = append(errs, err)
	}
	if c.HealthAddr, err = HealthAddr(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Common{}, err
	}
	return c, nil
}

// ValkeyAddr は Valkey の接続先を "host:port" の形で返す。
func (c Common) ValkeyAddr() string {
	return net.JoinHostPort(c.ValkeyHost, c.ValkeyPort)
}

// DefaultHealthAddr は healthcheck の口の既定（各コンテナのループバック。MVP の設計概要 §5）。
// コンテナごとにネットワークの名前空間が分かれているので、auth・acct・admin で同じポートを使う。
const DefaultHealthAddr = "127.0.0.1:9090"

// HealthAddr は healthcheck の口の待ち受けアドレス（RADIUS_HEALTH_ADDR）を返す。
// サーバーとコマンド healthcheck の両方が読む（コマンドは Valkey のパスワードなしで動かせるように、共通の設定とは分ける）。
func HealthAddr() (string, error) {
	addr := String("RADIUS_HEALTH_ADDR", DefaultHealthAddr)
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
		return "", fmt.Errorf("RADIUS_HEALTH_ADDR: must be host:port (got %q)", addr)
	}
	return addr, nil
}

// Audit は監査ログの設定（admin とコマンドで共通。MVP の設計概要 §13）。
type Audit struct {
	// Dir は日ごとのファイル（audit-YYYYMMDD.jsonl）を書くディレクトリ（RADIUS_AUDIT_DIR）。
	Dir string
	// RetentionDays はファイルを持つ日数（RADIUS_AUDIT_RETENTION_DAYS。既定 14）。
	RetentionDays int
	// MaxLen は Valkey の Stream（audit）に持つ件数の上限（RADIUS_AUDIT_MAX_LEN。既定 10000）。
	MaxLen int64
}

// LoadAudit は監査ログの設定を読む。
func LoadAudit() (Audit, error) {
	a := Audit{Dir: String("RADIUS_AUDIT_DIR", "/var/lib/aka-radius/audit")}
	days, err := PositiveInt("RADIUS_AUDIT_RETENTION_DAYS", 14)
	if err != nil {
		return Audit{}, err
	}
	if days > 3650 {
		return Audit{}, errors.New("RADIUS_AUDIT_RETENTION_DAYS: must be at most 3650")
	}
	a.RetentionDays = int(days)
	if a.MaxLen, err = PositiveInt("RADIUS_AUDIT_MAX_LEN", 10000); err != nil {
		return Audit{}, err
	}
	return a, nil
}
