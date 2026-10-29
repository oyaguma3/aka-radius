package config

import "time"

// Valkey接続設定（D-06準拠）
const (
	ValkeyConnectTimeout = 3 * time.Second
	ValkeyCommandTimeout = 2 * time.Second
	ValkeyPoolSize       = 10
)

// セッション管理（D-02準拠）
const (
	EAPContextTTL = 60 * time.Second
	SessionTTL    = 24 * time.Hour
)

// 再同期の上限。1 回の EAP の会話で 1 回まで（MVP の設計概要 §7.3。決定事項 9）
const (
	MaxResyncCount = 1
)

// サーバーシャットダウン設定（D-09 §3.5）
const (
	ShutdownTimeout = 5 * time.Second
)
