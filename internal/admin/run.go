// Package admin はサブコマンド admin（管理API 2.0.0。/admin/v2）の入口（MVP の設計概要 §11）。
// 本PoC の Provisioning API（/admin/v1、Gin）を置き換える。加入者の口（段階 2-3）、/sessions・/usage（2-4）、
// /clients・/sources（2-5）を持つ。
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/oyaguma3/aka-radius/internal/admin/api"
	"github.com/oyaguma3/aka-radius/internal/admin/auth"
	"github.com/oyaguma3/aka-radius/internal/admin/config"
	"github.com/oyaguma3/aka-radius/internal/audit"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/clients"
	"github.com/oyaguma3/aka-radius/internal/health"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	"github.com/oyaguma3/aka-radius/internal/usage"
	"github.com/oyaguma3/aka-radius/internal/valkey"
)

// Main はサブコマンド admin を動かす。build はビルドの版（起動ログと /status の build に出す。API の版とは別）。
// 設定・ピンのファイル・サーバー証明書・取得先の登録簿の誤り、監査ログのディレクトリに書けないとき、
// サーバーの異常では、終了コード 1 でプロセスを終える。
func Main(build string) {
	startedAt := time.Now()

	// 1. 設定読み込み
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("app", "admin").
			Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// 2. ロガー初期化
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.LogLevel)})).
		With("app", "admin")
	slog.SetDefault(log)
	log.Info("starting admin",
		"version", api.Version,
		"build", build,
		"listen_addr", cfg.ListenAddr,
		"log_level", cfg.LogLevel,
		"node_name", cfg.NodeName,
		"admin_clients_file", cfg.AdminClientsFile,
		"audit_dir", cfg.Audit.Dir,
		"audit_retention_days", cfg.Audit.RetentionDays,
		"audit_max_len", cfg.Audit.MaxLen,
		"sources_file", cfg.SourcesFile,
		"health_addr", cfg.HealthAddr,
	)

	// 3. 管理クライアントのピン、サーバー証明書、取得先の登録簿、監査ログのディレクトリ（どれかが駄目なら起動しない）
	pins, err := auth.LoadPins(cfg.AdminClientsFile)
	if err != nil {
		log.Error("failed to load admin clients", "event_id", "ADMIN_CLIENTS_LOAD_ERR", "file", cfg.AdminClientsFile, "error", err)
		os.Exit(1)
	}
	logPins(log, "loaded admin clients", "ADMIN_CLIENTS_LOADED", pins)
	certs, err := auth.LoadCertFile(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		log.Error("failed to load server certificate", "event_id", "SERVER_CERT_LOAD_ERR", "cert", cfg.TLSCert, "key", cfg.TLSKey, "error", err)
		os.Exit(1)
	}
	logCert(log, "loaded server certificate", "SERVER_CERT_LOADED", certs)
	sources, err := loadSources(cfg.SourcesFile)
	if err != nil {
		log.Error("failed to load sources registry", "event_id", "SOURCES_LOAD_ERR", "sources_file", cfg.SourcesFile, "error", err)
		os.Exit(1)
	}
	logSources(log, "loaded sources registry", "SOURCES_LOADED", sources)
	if err := audit.CheckWritableDir(cfg.Audit.Dir); err != nil {
		log.Error("audit directory is not writable", "event_id", "AUDIT_DIR_ERR", "error", err)
		os.Exit(1)
	}
	auditFiles := audit.NewFiles(cfg.Audit.Dir, cfg.Audit.RetentionDays)

	// 4. Valkey接続
	rdb, err := valkey.NewClient(valkey.DefaultOptions().WithAddr(cfg.ValkeyAddr()).WithPassword(cfg.ValkeyPassword))
	if err != nil {
		log.Error("failed to connect to Valkey", "addr", cfg.ValkeyAddr(), "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()
	log.Info("connected to Valkey", "addr", cfg.ValkeyAddr())

	// healthcheck の口（ループバック。Valkey に届くかを返す。MVP の設計概要 §5）
	healthSrv, err := health.Start(cfg.HealthAddr, func(ctx context.Context) error { return rdb.Ping(ctx).Err() }, log)
	if err != nil {
		log.Error("failed to listen for healthcheck", "event_id", "SYS_LISTEN_ERR", "addr", cfg.HealthAddr, "error", err)
		os.Exit(1)
	}
	defer func() { _ = healthSrv.Close() }()

	// 5. ハンドラー
	h := &api.Handler{
		Subs:            subscriber.NewStore(rdb),
		Sessions:        sessions.NewStore(rdb),
		Usage:           usage.NewStore(rdb),
		Clients:         clients.NewStore(rdb),
		SourceStatus:    source.NewStatusStore(rdb),
		Sources:         sources,
		Audit:           audit.NewRecorder(rdb, cfg.Audit.MaxLen, auditFiles, log),
		Log:             log,
		MgmtClient:      pins.NameFromRequest,
		AdminClients:    pins.Names,
		Build:           build,
		NodeName:        cfg.NodeName,
		StartedAt:       startedAt,
		StampVendorID:   cfg.StampVendorID,
		StampVendorType: cfg.StampVendorType,
	}
	verifier := &auth.Verifier{Pins: pins, Log: log}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           recoverer(log, h.Routes()),
		TLSConfig:         verifier.TLSConfig(certs),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// TLS ハンドシェイクの失敗等は DEBUG で残す（拒否した証明書は auth.Verifier が WARN で残す）
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	// 6. SIGHUP で、管理クライアントのピン、サーバー証明書、取得先の登録簿を読み直す
	// （それぞれ、失敗したら前のまま続ける。aka-vecgen の確認事項 E1 と同じ）
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			reload(log, pins, certs, sources)
		}
	}()

	// 監査ログのファイルの圧縮と削除（起動のときと 1 時間ごと）
	stop := make(chan struct{})
	defer close(stop)
	go maintainAudit(log, auditFiles, stop)

	// 7. サーバー起動
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Error("failed to listen", "event_id", "SYS_LISTEN_ERR", "addr", cfg.ListenAddr, "error", err)
		os.Exit(1)
	}
	go func() {
		log.Info("starting server", "addr", ln.Addr().String())
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// 8. シグナル待機と Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("server shutdown error", "error", err)
	}
	log.Info("server stopped")
}

// recoverer はハンドラーのパニックを回復して 500 を返し、ログ（SYS_PANIC）に出す。
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.Error("panic in admin handler", "event_id", "SYS_PANIC", "method", r.Method, "path", r.URL.Path, "panic", p)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"title":"Internal Server Error","status":500,"cause":"SYSTEM_FAILURE"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// reload は SIGHUP で、ピン・サーバー証明書・取得先の登録簿を読み直す。
func reload(log *slog.Logger, pins *auth.Pins, certs *auth.CertFile, sources *sourcesFile) {
	if err := pins.Reload(); err != nil {
		log.Error("failed to reload admin clients (keeping the previous ones)", "event_id", "ADMIN_CLIENTS_RELOAD_ERR", "error", err)
	} else {
		logPins(log, "reloaded admin clients", "ADMIN_CLIENTS_RELOADED", pins)
	}
	if err := certs.Reload(); err != nil {
		log.Error("failed to reload server certificate (keeping the previous one)", "event_id", "SERVER_CERT_RELOAD_ERR", "error", err)
	} else {
		logCert(log, "reloaded server certificate", "SERVER_CERT_RELOADED", certs)
	}
	if err := sources.reload(); err != nil {
		log.Error("failed to reload sources registry (keeping the previous one)", "event_id", "SOURCES_RELOAD_ERR", "error", err)
	} else {
		logSources(log, "reloaded sources registry", "SOURCES_RELOADED", sources)
	}
}

// auditMaintainInterval は監査ログのファイルを圧縮・削除する間隔。
const auditMaintainInterval = time.Hour

func maintainAudit(log *slog.Logger, files *audit.Files, stop <-chan struct{}) {
	t := time.NewTicker(auditMaintainInterval)
	defer t.Stop()
	for {
		if err := files.Maintain(time.Now()); err != nil {
			log.Error("failed to maintain audit files", "event_id", "AUDIT_MAINTAIN_ERR", "error", err)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func logPins(log *slog.Logger, msg, eventID string, pins *auth.Pins) {
	names, _ := pins.Names()
	log.Info(msg, "event_id", eventID, "names", names, "fingerprints", pins.Fingerprints())
}

func logCert(log *slog.Logger, msg, eventID string, certs *auth.CertFile) {
	leaf := certs.Leaf()
	log.Info(msg, "event_id", eventID, "subject", leaf.Subject.String(), "dns_names", leaf.DNSNames,
		"not_after", leaf.NotAfter.UTC(), "fingerprint", auth.Fingerprint(leaf))
}

// sourcesFile は取得先の登録簿（ID と証明書の期限だけ。鍵は読まない）。api.Sources の実装。
type sourcesFile struct {
	path     string
	mu       sync.RWMutex
	infos    []source.Info
	loadedAt time.Time
}

func loadSources(path string) (*sourcesFile, error) {
	s := &sourcesFile{path: path}
	return s, s.reload()
}

// reload は登録簿を読み直す。読めない・形が違うときは前のまま続け、エラーを返す。
func (s *sourcesFile) reload() error {
	infos, err := source.ReadInfos(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.infos, s.loadedAt = infos, time.Now()
	return nil
}

// Infos は取得先の情報と、読んだ時刻を返す。
func (s *sourcesFile) Infos() ([]source.Info, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.infos, s.loadedAt
}

func logSources(log *slog.Logger, msg, eventID string, s *sourcesFile) {
	infos, _ := s.Infos()
	ids := make([]string, len(infos))
	for i, in := range infos {
		ids[i] = in.ID
	}
	log.Info(msg, "event_id", eventID, "count", len(infos), "ids", ids)
}
