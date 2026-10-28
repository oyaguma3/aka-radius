// Package acct はサブコマンド acct（RADIUS Accounting サーバー）の入口。
package acct

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oyaguma3/aka-radius/internal/acct/config"
	"github.com/oyaguma3/aka-radius/internal/acct/processor"
	"github.com/oyaguma3/aka-radius/internal/acct/server"
	"github.com/oyaguma3/aka-radius/internal/acct/store"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/redis/go-redis/v9"
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
		"stamp_vendor_id", cfg.StampVendorID,
		"stamp_vendor_type", cfg.StampVendorType,
		"request_timeout_ms", cfg.Guard.RequestTimeout.Milliseconds(),
		"max_in_flight", cfg.Guard.MaxInFlight,
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

	// 4. RADIUS サーバー
	rdb := valkeyClient.Client()
	srv := newServer(cfg, rdb)

	// 5. セッションの索引の掃除とセッションの数え上げ（起動のときと 1 時間ごと。MVP の設計概要 §10.1）
	stop := make(chan struct{})
	defer close(stop)
	go sweepSessions(sessions.NewStore(rdb), sweepInterval, stop)

	// 6. サーバー起動、シグナル待機 → Graceful Shutdown
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

// newServer は acct の RADIUS サーバーを組み立てる（試験でも使う）。
// 重複の判定・増分・利用の集計・セッションの更新は 1 つの Lua で行う（MVP の設計概要 §10）。
// 受け口は、重複した要求への応答の再送、要求の期限、同時に処理する上限を受け持つ（§4・§9）。
func newServer(cfg *config.Config, rdb *redis.Client) *radiussrv.Server {
	proc := processor.New(store.NewRecordStore(rdb), radiussrv.NewStampTrust(rdb), cfg.LogMaskIMSI)
	handler := radiussrv.NewGuard("ACCT", cfg.Guard, server.NewHandler(proc, cfg.StampVendorID, cfg.StampVendorType))
	secretSource := radiussrv.NewSecretSource(radiussrv.NewClientStore(rdb), cfg.FallbackSecret)
	return radiussrv.NewServer(cfg.ListenAddr, handler, secretSource)
}

// sweepInterval はセッションの索引の掃除の間隔。
const sweepInterval = time.Hour

// sweeper はセッションの索引を掃除する（sessions.Store）。
type sweeper interface {
	Sweep(ctx context.Context, now time.Time) (int64, error)
}

// sweepSessions は、起動のときと interval ごとに、セッションの索引から期限を過ぎたものを消し、セッションの数を数える。
func sweepSessions(s sweeper, interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		sweepOnce(s)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func sweepOnce(s sweeper) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	n, err := s.Sweep(ctx, start)
	if err != nil {
		slog.Error("セッションの索引の掃除に失敗", "event_id", "SESSION_SWEEP_ERR", "error", err)
		return
	}
	slog.Info("セッションの索引を掃除した", "event_id", "SESSION_SWEPT", "sessions", n, "latency_ms", time.Since(start).Milliseconds())
}
