package radiussrv

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"
)

// Runner は Run が使う RADIUS サーバーの操作（試験で差し替える）。
type Runner interface {
	ListenAndServe() error
	Shutdown(ctx context.Context) error
}

// Run は RADIUS サーバーを起動し、停止のシグナルを受けたらグレースフルに停止して nil を返す。
// シグナルの前にサーバーが終わった（待ち受けの開始の失敗など）ときは、そのエラーを返す。
func Run(srv Runner, addr string, sigCh <-chan os.Signal, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("RADIUSサーバー起動", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err == nil {
			err = errors.New("RADIUS server stopped unexpectedly")
		}
		return err
	case sig := <-sigCh:
		slog.Info("シグナル受信、シャットダウン開始", "signal", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("シャットダウンエラー", "error", err)
	}
	return nil
}

// WarnFallbackSecret は、RADIUS_FALLBACK_SECRET（予備の共有シークレット）が設定されているときにWARNログを出す。
// 設定されていると、クライアント登録（client:{IP}）のない送信元IPからのパケットも
// このシークレットで受け付けるため、インターネットに公開するサーバーでは空にすることを促す。
func WarnFallbackSecret(fallbackSecret string) {
	if fallbackSecret == "" {
		return
	}
	slog.Warn("RADIUS_FALLBACK_SECRETが設定されているため、クライアント登録のない送信元IPのパケットも予備の共有シークレットで受け付ける（インターネットに公開する場合は空にすること）")
}
