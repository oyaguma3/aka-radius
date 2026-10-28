package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/oyaguma3/aka-radius/internal/auth/source/sourcetest"
	"github.com/oyaguma3/aka-radius/internal/health"
)

// setIO はコマンドの標準入力と標準出力を差し替える。
func setIO(t *testing.T, in string) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prevIn, prevOut := stdin, stdout
	stdin, stdout = strings.NewReader(in), &out
	t.Cleanup(func() { stdin, stdout = prevIn, prevOut })
	return &out
}

// setValkey は miniredis を立て、コマンドが接続するように環境変数を設定する。監査ログのディレクトリを返す。
func setValkey(t *testing.T) (*miniredis.Miniredis, string) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.RequireAuth("pw")
	host, port, _ := net.SplitHostPort(mr.Addr())
	dir := t.TempDir()
	t.Setenv("RADIUS_VALKEY_HOST", host)
	t.Setenv("RADIUS_VALKEY_PORT", port)
	t.Setenv("RADIUS_VALKEY_PASSWORD", "pw")
	t.Setenv("RADIUS_AUDIT_DIR", dir)
	return mr, dir
}

// auditLines は今日の監査ログのファイルの行を読む。
func auditLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(paths) != 1 {
		t.Fatalf("audit files = %v", paths)
	}
	f, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestClientCommand(t *testing.T) {
	mr, dir := setValkey(t)
	ctx := t.Context()
	secret := "0123456789abcdef-secret"

	// 標準入力の共有シークレットで登録する
	out := setIO(t, secret+"\n")
	if err := clientCommand(ctx, []string{"add", "-ip", "10.0.0.2", "-name", "edge-01", "-trust-stamp", "true", "-operator", "alice"}); err != nil {
		t.Fatal(err)
	}
	if mr.HGet("client:10.0.0.2", "secret") != secret || mr.HGet("client:10.0.0.2", "trust_stamp") != "1" {
		t.Errorf("client:10.0.0.2 = %v", mr.HGet("client:10.0.0.2", "secret"))
	}
	if strings.Contains(out.String(), secret) {
		t.Errorf("output shows the secret read from stdin: %s", out)
	}

	// -generate は作ったシークレットを表示する
	out = setIO(t, "")
	if err := clientCommand(ctx, []string{"add", "-ip", "::ffff:10.0.0.3", "-name", "edge-02", "-trust-stamp", "false", "-generate"}); err != nil {
		t.Fatal(err)
	}
	gen := mr.HGet("client:10.0.0.3", "secret")
	if len(gen) != 32 || !strings.Contains(out.String(), "secret: "+gen) {
		t.Errorf("generated secret %q, output %s", gen, out)
	}

	// 誤り
	for name, args := range map[string][]string{
		"重複":           {"add", "-ip", "10.0.0.2", "-name", "x", "-trust-stamp", "true", "-generate"},
		"刻印の指定なし":      {"add", "-ip", "10.0.0.4", "-name", "x", "-generate"},
		"IP でない":       {"add", "-ip", "edge", "-name", "x", "-trust-stamp", "true", "-generate"},
		"短いシークレット":     {"add", "-ip", "10.0.0.4", "-name", "x", "-trust-stamp", "true"},
		"変えるものがない":     {"update", "-ip", "10.0.0.2"},
		"ない IP の変更":    {"update", "-ip", "10.0.0.9", "-name", "x"},
		"ない IP の削除":    {"remove", "-ip", "10.0.0.9"},
		"余った引数":        {"list", "extra"},
		"知らないサブコマンド":   {"show"},
		"両方のシークレットの指定": {"update", "-ip", "10.0.0.2", "-generate", "-secret-stdin"},
	} {
		setIO(t, "short\n")
		if err := clientCommand(ctx, args); err == nil {
			t.Errorf("%s: %v accepted", name, args)
		}
	}

	// 一覧（シークレットは出さない）
	out = setIO(t, "")
	if err := clientCommand(ctx, []string{"list"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 || !strings.HasPrefix(lines[1], "10.0.0.2") ||
		!strings.Contains(lines[1], "true") || strings.Contains(out.String(), secret) || strings.Contains(out.String(), gen) {
		t.Errorf("list = %s", out)
	}

	// 変更と削除
	setIO(t, "new-secret-0123456789\n")
	if err := clientCommand(ctx, []string{"update", "-ip", "10.0.0.2", "-name", "edge-01b", "-trust-stamp", "false", "-secret-stdin"}); err != nil {
		t.Fatal(err)
	}
	if mr.HGet("client:10.0.0.2", "name") != "edge-01b" || mr.HGet("client:10.0.0.2", "trust_stamp") != "0" || mr.HGet("client:10.0.0.2", "secret") != "new-secret-0123456789" {
		t.Error("update was not applied")
	}
	setIO(t, "")
	if err := clientCommand(ctx, []string{"remove", "-ip", "10.0.0.3"}); err != nil {
		t.Fatal(err)
	}
	if mr.Exists("client:10.0.0.3") {
		t.Error("remove did not delete the key")
	}

	// 監査ログ（Stream と日ごとのファイル。シークレットは残さない）
	lines := auditLines(t, dir)
	var actions []string
	for _, l := range lines {
		actions = append(actions, l["action"].(string))
		if l["mgmtClient"] != "cli" {
			t.Errorf("mgmtClient = %v", l["mgmtClient"])
		}
	}
	if strings.Join(actions, ",") != "client.create,client.create,client.update,client.delete" || lines[0]["operator"] != "alice" || lines[1]["operator"] != "cli" {
		t.Errorf("audit = %v", lines)
	}
	if d := lines[2]["detail"].(map[string]any); d["secret"] != "changed" || d["name"].(map[string]any)["to"] != "edge-01b" {
		t.Errorf("update detail = %v", d)
	}
	raw, _ := json.Marshal(lines)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), gen) || strings.Contains(string(raw), "new-secret") {
		t.Errorf("audit contains a secret: %s", raw)
	}
	if entries, err := mr.Stream("audit"); err != nil || len(entries) != 4 {
		t.Errorf("audit stream = %d entries, %v", len(entries), err)
	}
}

func TestSourcesCheck(t *testing.T) {
	fake := sourcetest.Start(t, "vecgen")
	// 偽の取得先の証明書は 1 日で切れるので、期限の警告を切って確かめる
	out := setIO(t, "")
	if err := sourcesCommand(t.Context(), []string{"check", "-file", fake.RegistryFile, "-warn-days", "0"}); err != nil {
		t.Fatalf("sources check: %v\n%s", err, out)
	}
	for _, want := range []string{"vecgen (fake vecgen)", "client certificate: OK", "server CA: OK", "fingerprint ", "connection: OK (TLS accepted, HTTP 404)", "result: OK"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}

	// 期限が近いと警告だけ（誤りにしない）、過ぎていれば誤り
	out = setIO(t, "")
	if err := sourcesCommand(t.Context(), []string{"check", "-file", fake.RegistryFile}); err != nil || !strings.Contains(out.String(), "client certificate: WARN: expires in 0 days") {
		t.Errorf("near expiry: %v\n%s", err, out)
	}
	out = setIO(t, "")
	if n, err := checkSources(t.Context(), fake.RegistryFile, 30*24*time.Hour, time.Now().Add(365*24*time.Hour*5)); err != nil || n == 0 {
		t.Errorf("expired: %d %v\n%s", n, err, out)
	}
	if !strings.Contains(out.String(), "FAIL: expired") {
		t.Errorf("output = %s", out)
	}

	// 接続できない取得先
	fake.SetHandler(nil)
	data, _ := os.ReadFile(fake.RegistryFile)
	broken := strings.Replace(string(data), fake.BaseURL, "https://127.0.0.1:1/nudm-ueau/v1", 1)
	if err := os.WriteFile(fake.RegistryFile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	out = setIO(t, "")
	if err := sourcesCommand(t.Context(), []string{"check", "-file", fake.RegistryFile}); err == nil || !strings.Contains(out.String(), "connection: FAIL") {
		t.Errorf("unreachable source: %v\n%s", err, out)
	}

	// 形の誤り
	if err := os.WriteFile(fake.RegistryFile, []byte(`{"sources":[{"id":"BAD"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sourcesCommand(t.Context(), []string{"check", "-file", fake.RegistryFile}); err == nil {
		t.Error("broken registry accepted")
	}
	if err := sourcesCommand(t.Context(), nil); err == nil {
		t.Error("sources without check accepted")
	}
}

func TestAdminCertAndFingerprint(t *testing.T) {
	_, auditDir := setValkey(t)
	dir := t.TempDir()
	out := setIO(t, "")
	if err := adminCertCommand(t.Context(), []string{"create", "-hosts", "aka-radius-admin, 10.10.0.2", "-out", dir}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dns names:   aka-radius-admin") || !strings.Contains(out.String(), "ip:          10.10.0.2") {
		t.Errorf("output = %s", out)
	}
	info, err := os.Stat(filepath.Join(dir, "server.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("server.key = %v %v", info, err)
	}
	// 上書きは -force のときだけ
	if err := adminCertCommand(t.Context(), []string{"create", "-hosts", "x", "-out", dir}); err == nil {
		t.Error("overwrite without -force accepted")
	}

	// 表示と、標準入力からのフィンガープリントが一致する
	t.Setenv("RADIUS_ADMIN_TLS_CERT", filepath.Join(dir, "server.pem"))
	out = setIO(t, "")
	if err := adminCertCommand(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var fp string
	for l := range strings.SplitSeq(out.String(), "\n") {
		if v, ok := strings.CutPrefix(l, "fingerprint: "); ok {
			fp = v
		}
	}
	pemData, _ := os.ReadFile(filepath.Join(dir, "server.pem"))
	out = setIO(t, string(pemData))
	if err := fingerprintCommand(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != fp || len(fp) != 64 {
		t.Errorf("fingerprint = %q, admin-cert = %q", out, fp)
	}
	setIO(t, "not a pem")
	if err := fingerprintCommand(t.Context(), nil); err == nil {
		t.Error("fingerprint of a non-PEM accepted")
	}

	// 作った証明書は監査ログに残る
	if lines := auditLines(t, auditDir); len(lines) != 1 || lines[0]["action"] != "admin-server-certificate.create" || lines[0]["detail"].(map[string]any)["fingerprint"] != fp {
		t.Errorf("audit = %v", lines)
	}
}

func TestHealthcheckCommand(t *testing.T) {
	s, err := health.Start("127.0.0.1:0", func(ctx context.Context) error { return nil }, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADIUS_HEALTH_ADDR", s.Addr())
	if err := healthcheckCommand(t.Context(), nil); err != nil {
		t.Errorf("healthcheck = %v", err)
	}
	_ = s.Close()
	if err := healthcheckCommand(t.Context(), nil); err == nil {
		t.Error("healthcheck after Close: want error")
	}
}
