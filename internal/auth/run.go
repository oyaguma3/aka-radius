// Package auth はサブコマンド auth（EAP-AKA/AKA' の RADIUS 認証サーバー）の入口。
package auth

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/auth/engine"
	"github.com/oyaguma3/aka-radius/internal/auth/policy"
	"github.com/oyaguma3/aka-radius/internal/auth/server"
	"github.com/oyaguma3/aka-radius/internal/auth/session"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/auth/store"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
)

// Main はサブコマンド auth を動かす。build はビルドの版（起動ログの build に出す）。
// 設定の誤り、取得先の登録簿の誤り、待ち受けの失敗では、終了コード 1 でプロセスを終える。
func Main(build string) {
	// 1. 環境変数読み込み
	cfg, err := config.Load()
	if err != nil {
		slog.Error("設定読み込み失敗", "error", err)
		os.Exit(1)
	}

	// 2. ロガー初期化（JSON形式、RADIUS_LOG_LEVEL 以上。既定 INFO）
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logging.ParseLevel(cfg.LogLevel),
	})).With("app", "auth")
	slog.SetDefault(logger)

	slog.Info("auth起動開始",
		"build", build,
		"listen_addr", cfg.ListenAddr,
		"log_level", cfg.LogLevel,
		"sources_file", cfg.SourcesFile,
		"network_name", cfg.NetworkName,
		"fallback_secret", cfg.FallbackSecret != "",
	)
	radiussrv.WarnFallbackSecret(cfg.FallbackSecret)

	// 3. 取得先の登録簿（読めない・形が違うときは起動しない）
	registry, err := source.LoadRegistry(cfg.SourcesFile)
	if err != nil {
		slog.Error("取得先の登録簿の読み込み失敗",
			"event_id", "SOURCES_LOAD_ERR",
			"sources_file", cfg.SourcesFile,
			"error", err,
		)
		os.Exit(1)
	}
	logSources("取得先の登録簿を読み込んだ", "SOURCES_LOADED", registry)

	// 4. Valkeyクライアント初期化
	valkeyClient, err := store.NewValkeyClient(cfg)
	if err != nil {
		slog.Error("Valkey接続失敗",
			"event_id", "VALKEY_CONN_ERR",
			"error", err,
		)
		os.Exit(1)
	}
	defer valkeyClient.Close()

	slog.Info("Valkey接続完了", "addr", cfg.ValkeyAddr())

	// 5. RADIUS サーバー
	srv := newServer(cfg, valkeyClient, registry)

	// 6. SIGHUP で取得先の登録簿を読み直す（失敗したら前の登録簿のまま続ける）
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			reloadSources(registry)
		}
	}()

	// 7. サーバー起動、シグナル待機 → Graceful Shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	if err := radiussrv.Run(srv, cfg.ListenAddr, sigCh, config.ShutdownTimeout); err != nil {
		// 待ち受けを開始できない（ポートが使用中など）か、異常終了した。プロセスを終えて、compose の restart で
		// 起動し直させる（終了しないと、待ち受けていないのに pgrep の healthcheck が通ってしまう）
		slog.Error("サーバーエラー", "event_id", "SYS_LISTEN_ERR", "addr", cfg.ListenAddr, "error", err)
		os.Exit(1)
	}

	slog.Info("auth停止完了")
}

// newServer は auth の RADIUS サーバーを組み立てる（試験でも使う）。
func newServer(cfg *config.Config, valkeyClient *store.ValkeyClient, registry *source.Registry) *radiussrv.Server {
	fetcher := source.NewService(store.NewSubscriberStore(valkeyClient), registry)
	eapEngine := engine.NewEngine(
		fetcher,
		session.NewContextStore(valkeyClient),
		session.NewSessionStore(valkeyClient),
		store.NewPolicyStore(valkeyClient),
		policy.NewEvaluator(),
		cfg,
	)
	secretSource := radiussrv.NewSecretSource(radiussrv.NewClientStore(valkeyClient.Client()), cfg.FallbackSecret)
	return radiussrv.NewServer(cfg.ListenAddr, server.NewHandler(eapEngine), secretSource)
}

// reloadSources は取得先の登録簿を読み直し、結果をログに出す。
func reloadSources(registry *source.Registry) {
	if err := registry.Reload(); err != nil {
		slog.Error("取得先の登録簿の読み直し失敗（前の登録簿のまま続ける）",
			"event_id", "SOURCES_RELOAD_ERR",
			"error", err,
		)
		return
	}
	logSources("取得先の登録簿を読み直した", "SOURCES_RELOADED", registry)
}

// logSources は登録簿の取得先をログに出す（鍵のパスは出さない）。空なら WARN を出す。
func logSources(msg, eventID string, registry *source.Registry) {
	infos := registry.List()
	if len(infos) == 0 {
		slog.Warn(msg+"（取得先が 1 つもないので、すべての認証を拒否する）", "event_id", eventID, "count", 0)
		return
	}
	sources := make([]map[string]any, 0, len(infos))
	for _, in := range infos {
		sources = append(sources, map[string]any{
			"id":                  in.ID,
			"name":                in.Name,
			"base_url":            in.BaseURL,
			"timeout_ms":          in.Timeout.Milliseconds(),
			"client_cert_expires": in.NotAfter.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	slog.Info(msg, "event_id", eventID, "count", len(infos), "sources", sources)
}
