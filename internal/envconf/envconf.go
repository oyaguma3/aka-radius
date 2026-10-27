// Package envconf は環境変数の読み込みの共通処理を提供する（標準ライブラリだけで読む）。
// 環境変数の接頭辞は RADIUS_（MVP の設計概要 §4）。
package envconf

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	if err := errors.Join(errs...); err != nil {
		return Common{}, err
	}
	return c, nil
}

// ValkeyAddr は Valkey の接続先を "host:port" の形で返す。
func (c Common) ValkeyAddr() string {
	return net.JoinHostPort(c.ValkeyHost, c.ValkeyPort)
}
