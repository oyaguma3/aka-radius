// Package acct はサブコマンド acct（RADIUS Accounting サーバー）の入口。
package acct

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/oyaguma3/aka-radius/internal/acct/acct"
	"github.com/oyaguma3/aka-radius/internal/acct/config"
	"github.com/oyaguma3/aka-radius/internal/acct/server"
	"github.com/oyaguma3/aka-radius/internal/acct/session"
	"github.com/oyaguma3/aka-radius/internal/acct/store"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
)

// Main はサブコマンド acct を動かす。build はビルドの版（起動ログの build に出す）。
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
	})).With("app", "acct")
	slog.SetDefault(logger)

	slog.Info("acct起動開始",
		"build", build,
		"listen_addr", cfg.ListenAddr,
		"log_level", cfg.LogLevel,
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

	// 4. Store層生成
	clientStore := radiussrv.NewClientStore(valkeyClient.Client())
	sessionStore := store.NewSessionStore(valkeyClient)
	duplicateStore := store.NewDuplicateStore(valkeyClient)

	// 5. Session層生成
	sessionManager := session.NewManager(sessionStore)
	identifierResolver := session.NewIdentifierResolver(sessionManager, cfg.LogMaskIMSI)

	// 6. Acct層生成
	duplicateDetector := acct.NewDuplicateDetector(duplicateStore)
	processor := acct.NewProcessor(sessionManager, duplicateDetector, identifierResolver)

	// 7. RADIUS Secret解決
	secretSource := radiussrv.NewSecretSource(clientStore, cfg.FallbackSecret)

	// 8. RADIUSハンドラ
	handler := server.NewHandler(processor)

	// 9. UDPサーバー
	srv := radiussrv.NewServer(cfg.ListenAddr, handler, secretSource)

	// 10. サーバー起動、11. シグナル待機 → Graceful Shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	if err := radiussrv.Run(srv, cfg.ListenAddr, sigCh, config.ShutdownTimeout); err != nil {
		// 待ち受けを開始できない（ポートが使用中など）か、異常終了した。プロセスを終えて、compose の restart で
		// 起動し直させる（終了しないと、待ち受けていないのに pgrep の healthcheck が通ってしまう）
		slog.Error("サーバーエラー", "event_id", "SYS_LISTEN_ERR", "addr", cfg.ListenAddr, "error", err)
		os.Exit(1)
	}

	slog.Info("acct停止完了")
}
