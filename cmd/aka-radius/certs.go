package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oyaguma3/aka-radius/internal/admin/auth"
	"github.com/oyaguma3/aka-radius/internal/envconf"
)

const (
	// adminCertCN は自己署名のサーバー証明書の主体名。
	adminCertCN = "aka-radius-admin"
	// adminCertDays は自己署名のサーバー証明書の有効日数の既定（相手は証明書そのものを信頼の起点にするので長くする。aka-vecgen と同じ 10 年）。
	adminCertDays = 3650
	// adminReloadNotice は、動いている admin に反映するための案内。
	adminReloadNotice = "to apply it to the running admin: docker compose kill -s HUP admin"
)

// adminCertCommand は管理API のサーバー証明書を表示する。create で自己署名の証明書と鍵を作る（MVP の設計概要 §12。段階 2-5 の K8）。
func adminCertCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "show" {
		if len(args) > 1 {
			return errors.New("usage: aka-radius admin-cert [show]")
		}
		path := envconf.String("RADIUS_ADMIN_TLS_CERT", "/certs/server.pem")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		cert, err := parseCertPEM(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		_, _ = fmt.Fprintf(stdout, "file:        %s\n", path)
		describeCert(cert)
		return nil
	}
	if args[0] != "create" {
		return fmt.Errorf("unknown admin-cert command %q (show, create)", args[0])
	}
	fs := flag.NewFlagSet("admin-cert create", flag.ContinueOnError)
	hosts := fs.String("hosts", "", "SAN に入れるホスト名と IP（カンマ区切り。必須。例 aka-radius-admin,10.10.0.2）")
	out := fs.String("out", "", "server.pem と server.key を書くディレクトリ（必須）")
	days := fs.Int("days", adminCertDays, "有効日数")
	force := fs.Bool("force", false, "すでにあるファイルを上書きする")
	operator := operatorFlag(fs)
	if err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	if *out == "" || *hosts == "" {
		return errors.New("-hosts and -out are required")
	}
	if *days < 1 || *days > 3650 {
		return errors.New("-days must be 1 to 3650")
	}
	certPEM, keyPEM, err := selfSigned(splitHosts(*hosts), time.Duration(*days)*24*time.Hour, time.Now())
	if err != nil {
		return err
	}
	certPath, keyPath := filepath.Join(*out, "server.pem"), filepath.Join(*out, "server.key")
	if !*force {
		for _, p := range []string{certPath, keyPath} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s already exists (use -force to replace it)", p)
			}
		}
	}
	// 鍵を先に書く（証明書だけが新しくなって組が合わなくなるのを避ける。admin の読み直しは組が合わなければ前のまま）
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	cert, _ := parseCertPEM(certPEM)
	describeCert(cert)
	_, _ = fmt.Fprintf(stdout, "wrote %s and %s\n%s\n", certPath, keyPath, adminReloadNotice)

	// 監査ログに残す。Valkey に届かなければ（Valkey を起動する前に作るとき）、日ごとのファイルにだけ書く
	rdb, a, err := openValkey(*operator)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "note: Valkey is not reachable, so the audit log goes to the daily file only: %v\n", err)
		if a, err = fileOnlyAudit(*operator); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "warning: the audit log was not written: %v\n", err)
			return nil
		}
	} else {
		defer func() { _ = rdb.Close() }()
	}
	a.record(ctx, "admin-server-certificate.create", adminCertCN, map[string]any{
		"fingerprint": auth.Fingerprint(cert), "dnsNames": cert.DNSNames, "ipAddresses": ipStrings(cert.IPAddresses),
		"notAfter": cert.NotAfter.UTC(),
	})
	return nil
}

// fingerprintCommand は標準入力の証明書（PEM の最初の証明書）の SHA-256 のフィンガープリントを表示する
// （管理クライアントのピンのファイルに書く値。aka-vecgen の fingerprint と同じ）。
func fingerprintCommand(_ context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: aka-radius fingerprint < cert.pem")
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	cert, err := parseCertPEM(data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, auth.Fingerprint(cert))
	return nil
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		if block, data = pem.Decode(data); block == nil {
			return nil, errors.New("no PEM certificate found")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

func describeCert(c *x509.Certificate) {
	_, _ = fmt.Fprintf(stdout, "subject:     %s\n", c.Subject)
	_, _ = fmt.Fprintf(stdout, "dns names:   %s\n", strings.Join(c.DNSNames, ", "))
	_, _ = fmt.Fprintf(stdout, "ip:          %s\n", strings.Join(ipStrings(c.IPAddresses), ", "))
	_, _ = fmt.Fprintf(stdout, "not before:  %s\n", c.NotBefore.UTC().Format(time.RFC3339))
	_, _ = fmt.Fprintf(stdout, "not after:   %s\n", c.NotAfter.UTC().Format(time.RFC3339))
	_, _ = fmt.Fprintf(stdout, "fingerprint: %s\n", auth.Fingerprint(c))
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}

func splitHosts(v string) []string {
	var out []string
	for h := range strings.SplitSeq(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// selfSigned は管理API の自己署名のサーバー証明書（ECDSA P-256）と鍵（PKCS #8）を PEM で作る。
func selfSigned(hosts []string, validFor time.Duration, now time.Time) (certPEM, keyPEM []byte, err error) {
	if len(hosts) == 0 {
		return nil, nil, errors.New("at least one host is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: adminCertCN},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// writeFileAtomic は一時のファイルに書いてから名前を変える（読み直しの途中で半端なファイルを読ませない）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
