// Package config は環境変数から設定を読み込む（D-13 §8.1.1。環境変数の名前は MVP の設計概要 §4 の RADIUS_ に改めた）。
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/oyaguma3/aka-radius/internal/admin/auth"
	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// Config は管理API の設定を保持する。
type Config struct {
	// Valkey とログの設定（auth・acct・admin で共通）
	envconf.Common

	// サーバー設定
	ListenAddr string // RADIUS_ADMIN_LISTEN_ADDR（既定 :9444）
	TLSCert    string // RADIUS_ADMIN_TLS_CERT
	TLSKey     string // RADIUS_ADMIN_TLS_KEY
	NodeName   string // RADIUS_ADMIN_NODE_NAME（空ならホスト名）
	// AuditMax は Valkey の Stream（audit:prov）に保存する監査ログの件数の上限（RADIUS_ADMIN_AUDIT_MAX。D-13 §6.2）
	AuditMax int64

	// AdminClients は RADIUS_ADMIN_CLIENTS（「識別名=フィンガープリント」のカンマ区切り）を解釈した管理クライアント
	AdminClients auth.Clients
}

// Load は環境変数から設定を読み込む。
// 管理クライアントが1件もない場合や、証明書のパスが空の場合はエラーを返す（起動しない）。
func Load() (*Config, error) {
	common, err := envconf.LoadCommon()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	cfg := Config{
		Common:     common,
		ListenAddr: envconf.String("RADIUS_ADMIN_LISTEN_ADDR", ":9444"),
		TLSCert:    os.Getenv("RADIUS_ADMIN_TLS_CERT"),
		TLSKey:     os.Getenv("RADIUS_ADMIN_TLS_KEY"),
		NodeName:   os.Getenv("RADIUS_ADMIN_NODE_NAME"),
	}
	if cfg.TLSCert == "" || cfg.TLSKey == "" {
		return nil, errors.New("RADIUS_ADMIN_TLS_CERT and RADIUS_ADMIN_TLS_KEY must not be empty")
	}
	if cfg.AuditMax, err = envconf.PositiveInt("RADIUS_ADMIN_AUDIT_MAX", 10000); err != nil {
		return nil, err
	}

	clients, err := auth.ParseClients(os.Getenv("RADIUS_ADMIN_CLIENTS"))
	if err != nil {
		return nil, fmt.Errorf("RADIUS_ADMIN_CLIENTS: %w", err)
	}
	if len(clients) == 0 {
		return nil, errors.New("RADIUS_ADMIN_CLIENTS: at least one admin client is required")
	}
	cfg.AdminClients = clients

	if cfg.NodeName == "" {
		cfg.NodeName, _ = os.Hostname()
	}
	return &cfg, nil
}
