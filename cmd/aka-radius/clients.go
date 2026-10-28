package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oyaguma3/aka-radius/internal/clients"
)

// stdin・stdout はコマンドの入出力（試験で差し替える）。
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
)

// clientNotice は、変更がすぐ効くことの案内（auth・acct はパケットのたびに Valkey を引く）。
const clientNotice = "auth and acct use the change from the next packet (no reload needed)"

// clientCommand は RADIUS クライアント（エッジのトンネルの IP）を管理する（MVP の設計概要 §12。段階 2-5 の K2）。
// エッジを足すときに、WireGuard のピアの設定と一緒に行う。
func clientCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: aka-radius client add|list|update|remove [options]")
	}
	fs := flag.NewFlagSet("client "+args[0], flag.ContinueOnError)
	switch args[0] {
	case "add":
		ip := fs.String("ip", "", "RADIUS クライアント（エッジのトンネル）の IP（必須）")
		name := fs.String("name", "", "名前（英数字と . _ -、64 文字まで。必須）")
		trust := fs.String("trust-stamp", "", "テナントの刻印を信頼するか（true / false。必須。エッジは true）")
		generate := fs.Bool("generate", false, "共有シークレットをランダムに作って表示する（省くと標準入力の 1 行から読む）")
		operator := operatorFlag(fs)
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		c := clients.Client{Name: *name}
		var err error
		if c.IP, err = clients.NormalizeIP(*ip); err != nil {
			return err
		}
		if err := clients.CheckName(c.Name); err != nil {
			return err
		}
		if *trust == "" {
			return errors.New("-trust-stamp is required (true or false)")
		}
		if c.TrustStamp, err = parseTrust(*trust); err != nil {
			return err
		}
		if c.Secret, err = readSecret(*generate); err != nil {
			return err
		}
		rdb, a, err := openValkey(*operator)
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()
		if err := clients.NewStore(rdb).Add(ctx, c); err != nil {
			if errors.Is(err, clients.ErrExists) {
				return fmt.Errorf("radius client %s already exists (use client update)", c.IP)
			}
			return err
		}
		a.record(ctx, "client.create", c.IP, map[string]any{"name": c.Name, "trustStamp": c.TrustStamp})
		_, _ = fmt.Fprintf(stdout, "added radius client %s (name %s, trust-stamp %t)\n", c.IP, c.Name, c.TrustStamp)
		if *generate {
			_, _ = fmt.Fprintf(stdout, "secret: %s\n(set the same secret on the edge; it is not shown again)\n", c.Secret)
		}
		_, _ = fmt.Fprintln(stdout, clientNotice)
		return nil

	case "list":
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		rdb, _, err := openValkey("")
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()
		list, err := clients.NewStore(rdb).List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "IP\tNAME\tTRUST-STAMP\tCREATED\tUPDATED")
		for _, c := range list {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n", c.IP, c.Name, c.TrustStamp, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
		}
		return tw.Flush()

	case "update":
		ip := fs.String("ip", "", "RADIUS クライアントの IP（必須）")
		name := fs.String("name", "", "新しい名前")
		trust := fs.String("trust-stamp", "", "テナントの刻印を信頼するか（true / false）")
		secretStdin := fs.Bool("secret-stdin", false, "共有シークレットを標準入力の 1 行から読んで変える")
		generate := fs.Bool("generate", false, "共有シークレットをランダムに作り直して表示する")
		operator := operatorFlag(fs)
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		addr, err := clients.NormalizeIP(*ip)
		if err != nil {
			return err
		}
		if *secretStdin && *generate {
			return errors.New("use either -secret-stdin or -generate")
		}
		var u clients.Update
		if *name != "" {
			if err := clients.CheckName(*name); err != nil {
				return err
			}
			u.Name = name
		}
		if *trust != "" {
			b, err := parseTrust(*trust)
			if err != nil {
				return err
			}
			u.TrustStamp = &b
		}
		if *secretStdin || *generate {
			secret, err := readSecret(*generate)
			if err != nil {
				return err
			}
			u.Secret = &secret
		}
		if u.Name == nil && u.TrustStamp == nil && u.Secret == nil {
			return errors.New("nothing to update: give -name, -trust-stamp, -secret-stdin or -generate")
		}
		rdb, a, err := openValkey(*operator)
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()
		st := clients.NewStore(rdb)
		before, err := st.Get(ctx, addr)
		if err != nil {
			return notFound(addr, err)
		}
		if err := st.Update(ctx, addr, u); err != nil {
			return notFound(addr, err)
		}
		// 監査ログには共有シークレットそのものを残さない
		detail := map[string]any{}
		if u.Name != nil {
			detail["name"] = change{before.Name, *u.Name}
		}
		if u.TrustStamp != nil {
			detail["trustStamp"] = change{before.TrustStamp, *u.TrustStamp}
		}
		if u.Secret != nil {
			detail["secret"] = "changed"
		}
		a.record(ctx, "client.update", addr, detail)
		_, _ = fmt.Fprintf(stdout, "updated radius client %s\n", addr)
		if *generate {
			_, _ = fmt.Fprintf(stdout, "secret: %s\n(set the same secret on the edge; it is not shown again)\n", *u.Secret)
		}
		_, _ = fmt.Fprintln(stdout, clientNotice)
		return nil

	case "remove":
		ip := fs.String("ip", "", "RADIUS クライアントの IP（必須）")
		operator := operatorFlag(fs)
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		addr, err := clients.NormalizeIP(*ip)
		if err != nil {
			return err
		}
		rdb, a, err := openValkey(*operator)
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()
		st := clients.NewStore(rdb)
		before, err := st.Get(ctx, addr)
		if err != nil {
			return notFound(addr, err)
		}
		if err := st.Remove(ctx, addr); err != nil {
			return notFound(addr, err)
		}
		a.record(ctx, "client.delete", addr, map[string]any{"name": before.Name, "trustStamp": before.TrustStamp})
		_, _ = fmt.Fprintf(stdout, "removed radius client %s (%s)\n", addr, before.Name)
		_, _ = fmt.Fprintln(stdout, clientNotice)
		return nil
	}
	return fmt.Errorf("unknown client command %q (add, list, update, remove)", args[0])
}

func parseTrust(v string) (bool, error) {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, errors.New("-trust-stamp must be true or false")
	}
	return b, nil
}

// readSecret は共有シークレットを作るか、標準入力の 1 行から読む（引数に書くとシェルの履歴と ps に残るため。aka-vecgen の F8）。
func readSecret(generate bool) (string, error) {
	if generate {
		return clients.GenerateSecret(), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read secret from stdin: %w", err)
	}
	secret := strings.TrimRight(line, "\r\n")
	if err := clients.CheckSecret(secret); err != nil {
		return "", fmt.Errorf("secret from stdin: %w", err)
	}
	return secret, nil
}

func notFound(ip string, err error) error {
	if errors.Is(err, clients.ErrNotFound) {
		return fmt.Errorf("radius client %s not found", ip)
	}
	return err
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
