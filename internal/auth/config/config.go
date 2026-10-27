package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// Config はアプリケーション設定を保持する
type Config struct {
	// Valkey とログの設定（auth・acct・admin で共通）
	envconf.Common

	// Vector Gateway設定（VECTOR_API_URL。段階 2-2 で取得先の登録簿に置き換えるので、名前を変えない）
	VectorAPIURL string

	// RADIUS設定
	// FallbackSecret は予備の共有シークレット（RADIUS_FALLBACK_SECRET）。空ならクライアント登録のない送信元を捨てる
	FallbackSecret string
	// ListenAddr は待ち受けアドレス（RADIUS_AUTH_LISTEN_ADDR、既定 :1812）
	ListenAddr string

	// NetworkName は EAP-AKA' のネットワーク名（RADIUS_EAP_AKA_PRIME_NETWORK_NAME、既定 WLAN）
	NetworkName string
}

// Load は環境変数から設定を読み込む
func Load() (*Config, error) {
	common, err := envconf.LoadCommon()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	cfg := Config{
		Common:         common,
		FallbackSecret: envconf.String("RADIUS_FALLBACK_SECRET", ""),
		ListenAddr:     envconf.String("RADIUS_AUTH_LISTEN_ADDR", ":1812"),
		NetworkName:    envconf.String("RADIUS_EAP_AKA_PRIME_NETWORK_NAME", "WLAN"),
	}
	if cfg.VectorAPIURL, err = envconf.Required("VECTOR_API_URL"); err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return &cfg, nil
}

// validate は設定値のバリデーションを行う
func (c *Config) validate() error {
	if strings.TrimSpace(c.NetworkName) == "" {
		return errors.New("RADIUS_EAP_AKA_PRIME_NETWORK_NAME must not be empty")
	}
	if !strings.HasPrefix(c.VectorAPIURL, "http://") && !strings.HasPrefix(c.VectorAPIURL, "https://") {
		return errors.New("VECTOR_API_URL must start with http:// or https://")
	}
	return nil
}
