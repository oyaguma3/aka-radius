// Package config は管理API の設定を環境変数から読み込む（MVP の設計概要 §11。環境変数の接頭辞は RADIUS_）。
package config

import (
	"fmt"
	"os"

	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// Config は管理API の設定を保持する。
type Config struct {
	// Valkey とログの設定（auth・acct・admin で共通）
	envconf.Common
	// Audit は監査ログの設定（コマンドと共通）
	Audit envconf.Audit

	// サーバー設定
	ListenAddr string // RADIUS_ADMIN_LISTEN_ADDR（既定 :9444）
	TLSCert    string // RADIUS_ADMIN_TLS_CERT（既定 /certs/server.pem。SIGHUP で読み直す）
	TLSKey     string // RADIUS_ADMIN_TLS_KEY（既定 /certs/server.key）
	NodeName   string // RADIUS_ADMIN_NODE_NAME（空ならホスト名）

	// AdminClientsFile は管理クライアントのピンのファイル（RADIUS_ADMIN_CLIENTS_FILE。1 行に「識別名=フィンガープリント」。
	// 既定 /run/secrets/admin-clients。SIGHUP で読み直す。aka-vecgen の決定事項 7）
	AdminClientsFile string
	// SourcesFile は取得先の登録簿（RADIUS_SOURCES_FILE。auth と同じファイルを読み取り専用で読む。段階 2-3 の G4）
	SourcesFile string
	// StampVendorID・StampVendorType はテナントの刻印の Vendor-Id と Vendor-Type（/status に出す。auth と同じ環境変数）
	StampVendorID   uint32
	StampVendorType byte
}

// Load は環境変数から設定を読み込む。ピンのファイルと証明書は起動のときに読む（admin.Main）。
func Load() (*Config, error) {
	common, err := envconf.LoadCommon()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	cfg := Config{
		Common:           common,
		ListenAddr:       envconf.String("RADIUS_ADMIN_LISTEN_ADDR", ":9444"),
		TLSCert:          envconf.String("RADIUS_ADMIN_TLS_CERT", "/certs/server.pem"),
		TLSKey:           envconf.String("RADIUS_ADMIN_TLS_KEY", "/certs/server.key"),
		NodeName:         os.Getenv("RADIUS_ADMIN_NODE_NAME"),
		AdminClientsFile: envconf.String("RADIUS_ADMIN_CLIENTS_FILE", "/run/secrets/admin-clients"),
		SourcesFile:      envconf.String("RADIUS_SOURCES_FILE", "/etc/aka-radius/sources/sources.json"),
	}
	if cfg.StampVendorID, cfg.StampVendorType, err = envconf.StampVendor(); err != nil {
		return nil, err
	}
	if cfg.Audit, err = envconf.LoadAudit(); err != nil {
		return nil, err
	}
	if cfg.NodeName == "" {
		cfg.NodeName, _ = os.Hostname()
	}
	return &cfg, nil
}
