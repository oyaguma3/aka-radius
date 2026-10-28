package main

import (
	"context"
	"errors"

	"github.com/oyaguma3/aka-radius/internal/envconf"
	"github.com/oyaguma3/aka-radius/internal/health"
)

// healthcheckCommand は、同じコンテナで動いているサーバーの healthcheck の口（ループバック）に問い合わせる
// （MVP の設計概要 §5。compose の healthcheck が使う。distroless のイメージには curl も pgrep もない）。
// 口は Valkey に届くかを返すので、サーバーが待ち受けていない・Valkey に届かないときに失敗する。
func healthcheckCommand(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: aka-radius healthcheck")
	}
	addr, err := envconf.HealthAddr()
	if err != nil {
		return err
	}
	return health.Check(ctx, addr)
}
