// Command aka-radius は EAP-AKA / EAP-AKA' の RADIUS サーバー。
// サーバーのサブコマンドごとに別々のコンテナで動かす（同じイメージ。MVP の設計概要 §4）。
// 運用のコマンド（client・sources・admin-cert・fingerprint・healthcheck）は、動いているコンテナの中で使う（§12）。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/oyaguma3/aka-radius/internal/acct"
	"github.com/oyaguma3/aka-radius/internal/admin"
	"github.com/oyaguma3/aka-radius/internal/auth"
)

// build はビルドの版（git のコミットなど）。起動ログの build に出す。
// イメージのビルドで -ldflags "-X main.build=..." として埋め込む（compose の RADIUS_VERSION。既定 dev）。
var build = "dev"

// servers はサーバーのサブコマンドと、その入口。
var servers = map[string]func(build string){
	"auth":  auth.Main,
	"acct":  acct.Main,
	"admin": admin.Main,
}

// commands は運用のコマンドと、その入口（args はコマンドの名前より後の引数）。
var commands = map[string]func(ctx context.Context, args []string) error{
	"client":      clientCommand,
	"sources":     sourcesCommand,
	"admin-cert":  adminCertCommand,
	"fingerprint": fingerprintCommand,
	"healthcheck": healthcheckCommand,
}

const usage = `使い方: aka-radius <サブコマンド> [引数]

サーバー:
  auth      RADIUS 認証サーバー（1812/udp。EAP-AKA / EAP-AKA'）
  acct      RADIUS Accounting サーバー（1813/udp）
  admin     管理API（9444/tcp、mTLS）

コマンド（動いているコンテナの中で使う。例 docker compose exec admin aka-radius client list）:
  client add|list|update|remove   RADIUS クライアント（エッジ）の登録・一覧・変更・削除
  sources check                   取得先の登録簿の検査と、各取得先への接続の確かめ（auth のコンテナで）
  admin-cert [create]             管理API のサーバー証明書の表示と、自己署名の証明書の作成
  fingerprint                     標準入力の証明書（PEM）の SHA-256 フィンガープリント
  healthcheck                     このコンテナのサーバーの healthcheck（compose の healthcheck が使う）

設定は環境変数で渡す（README を参照）。各コマンドの引数は -h で表示する。
`

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run はサブコマンドを動かし、終了コードを返す。
func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	if server, ok := servers[args[0]]; ok {
		if len(args) != 1 {
			_, _ = fmt.Fprintf(stderr, "サーバーのサブコマンド %q は引数をとりません\n\n%s", args[0], usage)
			return 2
		}
		server(build)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "不明なサブコマンド: %q\n\n%s", args[0], usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cmd(ctx, args[1:]); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}
