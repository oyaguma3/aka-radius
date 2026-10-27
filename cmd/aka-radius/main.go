// Command aka-radius は EAP-AKA / EAP-AKA' の RADIUS サーバー。
// サブコマンドごとに別々のコンテナで動かす（同じイメージ。MVP の設計概要 §4）。
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/oyaguma3/aka-radius/internal/acct"
	"github.com/oyaguma3/aka-radius/internal/admin"
	"github.com/oyaguma3/aka-radius/internal/auth"
	"github.com/oyaguma3/aka-radius/internal/gateway"
)

// build はビルドの版（git のコミットなど）。起動ログの build に出す。
// イメージのビルドで -ldflags "-X main.build=..." として埋め込む（compose の RADIUS_VERSION。既定 dev）。
var build = "dev"

// subcommands はサブコマンドと、その入口。
var subcommands = map[string]func(build string){
	"auth":    auth.Main,
	"acct":    acct.Main,
	"admin":   admin.Main,
	"gateway": gateway.Main,
}

const usage = `使い方: aka-radius <サブコマンド>

サブコマンド:
  auth      RADIUS 認証サーバー（1812/udp。EAP-AKA / EAP-AKA'）
  acct      RADIUS Accounting サーバー（1813/udp）
  admin     管理API（9444/tcp、mTLS）
  gateway   Vector Gateway（8080/tcp。段階 2-2 で auth に取り込んでなくす）

設定は環境変数で渡す（README を参照）。
`

func main() {
	run, ok := lookup(os.Args[1:], os.Stderr)
	if !ok {
		os.Exit(2)
	}
	run(build)
}

// lookup は引数からサブコマンドの入口を引く。引数が誤っていれば使い方を w に書いて false を返す。
func lookup(args []string, w io.Writer) (func(string), bool) {
	if len(args) != 1 {
		_, _ = fmt.Fprint(w, usage)
		return nil, false
	}
	run, ok := subcommands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(w, "不明なサブコマンド: %q\n\n%s", args[0], usage)
		return nil, false
	}
	return run, true
}
