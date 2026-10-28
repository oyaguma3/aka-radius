package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/envconf"
)

// sourcesCommand は取得先の登録簿を確かめる（MVP の設計概要 §12。段階 2-5 の K6）。
// 鍵を持つ auth のコンテナで動かす。新しい登録簿を SIGHUP の前に確かめるときは
// docker compose run --rm --no-deps auth sources check のように、動いている auth とは別に動かす。
func sourcesCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("usage: aka-radius sources check [-file path] [-warn-days n]")
	}
	fs := flag.NewFlagSet("sources check", flag.ContinueOnError)
	file := fs.String("file", envconf.String("RADIUS_SOURCES_FILE", "/etc/aka-radius/sources/sources.json"), "取得先の登録簿のファイル")
	warnDays := fs.Int("warn-days", 30, "証明書の期限がこの日数より近ければ警告する")
	if err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	problems, err := checkSources(ctx, *file, time.Duration(*warnDays)*24*time.Hour, time.Now())
	if err != nil {
		return err
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	_, _ = fmt.Fprintln(stdout, "result: OK")
	return nil
}

// checkSources は登録簿を読み（形、証明書と鍵の組、サーバーの CA）、各取得先の証明書の期限と接続を確かめて、結果を書く。
// 誤りの数を返す（期限が近いだけなら警告で、数えない）。登録簿が読めなければ誤りを返す。
func checkSources(ctx context.Context, file string, warn time.Duration, now time.Time) (int, error) {
	reg, err := source.LoadRegistry(file)
	if err != nil {
		return 0, fmt.Errorf("sources registry %s: %w", file, err)
	}
	_, _ = fmt.Fprintf(stdout, "sources registry %s: format, certificates and keys OK\n", file)
	problems := 0
	for _, in := range reg.List() {
		_, _ = fmt.Fprintf(stdout, "\n%s (%s) %s timeout %s\n", in.ID, in.Name, in.BaseURL, in.Timeout)
		problems += reportExpiry("client certificate", in.ClientCertSubject, in.NotAfter, now, warn)
		_, _ = fmt.Fprintf(stdout, "    fingerprint %s\n", in.ClientCertFingerprint)
		for _, ca := range in.ServerCAs {
			problems += reportExpiry("server CA", ca.Subject, ca.NotAfter, now, warn)
		}
		c, _ := reg.Get(in.ID)
		pctx, cancel := context.WithTimeout(ctx, in.Timeout)
		code, err := c.Probe(pctx)
		cancel()
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "  connection: FAIL: %v\n", err)
			problems++
			continue
		}
		_, _ = fmt.Fprintf(stdout, "  connection: OK (TLS accepted, HTTP %d)\n", code)
	}
	return problems, nil
}

func reportExpiry(label, subject string, notAfter, now time.Time, warn time.Duration) int {
	left := notAfter.Sub(now)
	switch {
	case left <= 0:
		_, _ = fmt.Fprintf(stdout, "  %s: FAIL: expired at %s (%s)\n", label, notAfter.UTC().Format(time.RFC3339), subject)
		return 1
	case left < warn:
		_, _ = fmt.Fprintf(stdout, "  %s: WARN: expires in %d days at %s (%s)\n", label, int(left.Hours()/24), notAfter.UTC().Format(time.RFC3339), subject)
	default:
		_, _ = fmt.Fprintf(stdout, "  %s: OK: expires at %s (%s)\n", label, notAfter.UTC().Format(time.RFC3339), subject)
	}
	return 0
}
