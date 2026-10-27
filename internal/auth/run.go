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
	"github.com/oyaguma3/aka-radius/internal/auth/store"
	"github.com/oyaguma3/aka-radius/internal/auth/vector"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
)

// Main はサブコマンド auth を動かす。build はビルドの版（起動ログの build に出す）。
// 設定の誤りや待ち受けの失敗では、終了コード 1 でプロセスを終える。
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
		"vector_api_url", cfg.VectorAPIURL,
		"network_name", cfg.NetworkName,
		"fallback_secret", cfg.FallbackSecret != "",
	)
	radiussrv.WarnFallbackSecret(cfg.FallbackSecret)

	// 3. Valkeyクライアント初期化
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

	// 4. Vector Gatewayクライアント初期化
	vectorClient := vector.NewClient(cfg)

	// 5. Store/Session層生成
	clientStore := radiussrv.NewClientStore(valkeyClient.Client())
	policyStore := store.NewPolicyStore(valkeyClient)
	ctxStore := session.NewContextStore(valkeyClient)
	sessStore := session.NewSessionStore(valkeyClient)

	// 6. ポリシー評価器
	evaluator := policy.NewEvaluator()

	// 7. EAPエンジン
	eapEngine := engine.NewEngine(vectorClient, ctxStore, sessStore, policyStore, evaluator, cfg)

	// 8. RADIUS Secret解決
	secretSource := radiussrv.NewSecretSource(clientStore, cfg.FallbackSecret)

	// 9. RADIUSハンドラ
	handler := server.NewHandler(eapEngine)

	// 10. UDPサーバー
	srv := radiussrv.NewServer(cfg.ListenAddr, handler, secretSource)

	// 11. サーバー起動、12. シグナル待機 → Graceful Shutdown
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
