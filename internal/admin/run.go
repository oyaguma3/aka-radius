// Package admin はサブコマンド admin（管理API 2.0.0。/admin/v2）の入口（MVP の設計概要 §11）。
// 本PoC の Provisioning API（/admin/v1、Gin）を置き換える。段階 2-3 では加入者の口と /status・/audit-logs を持つ。
package admin

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/oyaguma3/aka-radius/internal/admin/api"
	"github.com/oyaguma3/aka-radius/internal/admin/audit"
	"github.com/oyaguma3/aka-radius/internal/admin/auth"
	"github.com/oyaguma3/aka-radius/internal/admin/config"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	"github.com/oyaguma3/aka-radius/internal/valkey"
)

// Main はサブコマンド admin を動かす。build はビルドの版（起動ログと /status の build に出す。API の版とは別）。
// 設定の誤り、取得先の登録簿の誤り、サーバーの異常では、終了コード 1 でプロセスを終える。
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
		"admin_clients", len(cfg.AdminClients),
		"audit_max", cfg.AuditMax,
		"sources_file", cfg.SourcesFile,
	)

	// 3. サーバー証明書と取得先の登録簿の読み込み
	cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		log.Error("failed to load server certificate", "cert", cfg.TLSCert, "key", cfg.TLSKey, "error", err)
		os.Exit(1)
	}
	sources, err := loadSources(cfg.SourcesFile)
	if err != nil {
		log.Error("failed to load sources registry", "event_id", "SOURCES_LOAD_ERR", "sources_file", cfg.SourcesFile, "error", err)
		os.Exit(1)
	}
	logSources(log, "loaded sources registry", "SOURCES_LOADED", sources)

	// 4. Valkey接続
	rdb, err := valkey.NewClient(valkey.DefaultOptions().WithAddr(cfg.ValkeyAddr()).WithPassword(cfg.ValkeyPassword))
	if err != nil {
		log.Error("failed to connect to Valkey", "addr", cfg.ValkeyAddr(), "error", err)
		os.Exit(1)
	}
	defer func() { _ = rdb.Close() }()
	log.Info("connected to Valkey", "addr", cfg.ValkeyAddr())

	// 5. ハンドラー
	h := &api.Handler{
		Subs:            subscriber.NewStore(rdb),
		Sources:         sources,
		Audit:           audit.NewRecorder(rdb, cfg.AuditMax, log),
		Log:             log,
		MgmtClient:      cfg.AdminClients.NameFromRequest,
		AdminClients:    slices.Sorted(maps.Values(cfg.AdminClients)),
		Build:           build,
		NodeName:        cfg.NodeName,
		StartedAt:       startedAt,
		StampVendorID:   cfg.StampVendorID,
		StampVendorType: cfg.StampVendorType,
	}
	verifier := &auth.Verifier{Clients: cfg.AdminClients, Log: log}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           recoverer(log, h.Routes()),
		TLSConfig:         verifier.TLSConfig(cert),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// TLS ハンドシェイクの失敗等は DEBUG で残す（拒否した証明書は auth.Verifier が WARN で残す）
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	// 6. SIGHUP で取得先の登録簿を読み直す（失敗したら前の登録簿のまま続ける）
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			if err := sources.reload(); err != nil {
				log.Error("failed to reload sources registry (keeping the previous one)", "event_id", "SOURCES_RELOAD_ERR", "error", err)
				continue
			}
			logSources(log, "reloaded sources registry", "SOURCES_RELOADED", sources)
		}
	}()

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
