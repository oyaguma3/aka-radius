package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/oyaguma3/aka-radius/internal/audit"
	"github.com/oyaguma3/aka-radius/internal/envconf"
	"github.com/oyaguma3/aka-radius/internal/valkey"
	"github.com/redis/go-redis/v9"
)

// operatorFlag は監査ログに残す操作者の名前のフラグを足す。
func operatorFlag(fs *flag.FlagSet) *string {
	return fs.String("operator", "cli", "監査ログに残す操作者の名前")
}

// parseFlags は引数を読む。余った引数があれば誤りにする。
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return nil
}

// cliAudit はコマンドの操作を監査ログ（Stream と日ごとのファイル）に残す。管理クライアントは cli。
type cliAudit struct {
	rec      *audit.Recorder
	operator string
}

func (a cliAudit) record(ctx context.Context, action, target string, detail any) {
	e := audit.Entry{Operator: a.operator, MgmtClient: "cli", Action: action, Target: target}
	if detail != nil {
		b, _ := json.Marshal(detail)
		e.Detail = string(b)
	}
	a.rec.Record(ctx, e)
}

// openValkey は共通の設定と監査ログの設定を読み、Valkey に接続して監査ログの書き手を作る。
func openValkey(operator string) (*redis.Client, cliAudit, error) {
	common, err := envconf.LoadCommon()
	if err != nil {
		return nil, cliAudit{}, err
	}
	auditCfg, err := envconf.LoadAudit()
	if err != nil {
		return nil, cliAudit{}, err
	}
	rdb, err := valkey.NewClient(valkey.DefaultOptions().WithAddr(common.ValkeyAddr()).WithPassword(common.ValkeyPassword))
	if err != nil {
		return nil, cliAudit{}, fmt.Errorf("connect to Valkey %s: %w", common.ValkeyAddr(), err)
	}
	// 監査ログの書き出しの失敗だけを標準エラーに出す（"audit" の行は出さない）
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rec := audit.NewRecorder(rdb, auditCfg.MaxLen, audit.NewFiles(auditCfg.Dir, auditCfg.RetentionDays), log)
	return rdb, cliAudit{rec: rec, operator: operator}, nil
}

type change struct {
	From any `json:"from"`
	To   any `json:"to"`
}
