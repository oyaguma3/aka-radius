package config

import (
	"fmt"

	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// Config はアプリケーション設定を保持する
type Config struct {
	// Valkey とログの設定（auth・acct・admin で共通）
	envconf.Common

	// RADIUS設定
	// FallbackSecret は予備の共有シークレット（RADIUS_FALLBACK_SECRET）。空ならクライアント登録のない送信元を捨てる
	FallbackSecret string
	// ListenAddr は待ち受けアドレス（RADIUS_ACCT_LISTEN_ADDR、既定 :1813）
	ListenAddr string
}

// Load は環境変数から設定を読み込む
func Load() (*Config, error) {
	common, err := envconf.LoadCommon()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return &Config{
		Common:         common,
		FallbackSecret: envconf.String("RADIUS_FALLBACK_SECRET", ""),
		ListenAddr:     envconf.String("RADIUS_ACCT_LISTEN_ADDR", ":1813"),
	}, nil
}
