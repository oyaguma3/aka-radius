package radiussrv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"

	"github.com/oyaguma3/aka-radius/internal/logging"
	"layeh.com/radius"
)

// Server はRADIUS UDPサーバーのラッパー
type Server struct {
	ps *radius.PacketServer

	// mu・closing・inflight は、dispatch で渡した処理を数え、停止のときに待つ（Shutdown）
	mu       sync.Mutex
	closing  bool
	inflight sync.WaitGroup
}

// NewServer は新しいServerを生成する
func NewServer(addr string, handler radius.Handler, secretSource radius.SecretSource) *Server {
	s := &Server{}
	s.ps = &radius.PacketServer{
		Addr:         addr,
		SecretSource: secretSource,
		Handler:      s.dispatch(recoverHandler(handler)),
		// パケットの認証（Request Authenticator / Message-Authenticator）はハンドラーで検証し、
		// 失敗したら送信元IP付きのログを出して破棄する。ライブラリにも検証させると、
		// Accounting-Request などのシークレット不一致や未知のCodeのパケットが、
		// ハンドラーに届く前に素のテキストのログだけで捨てられてしまうため、ライブラリの検証は使わない
		InsecureSkipVerify: true,
		// ライブラリが出すエラー（パケットの解析失敗など）を JSON の slog に流す
		ErrorLog: logging.NewRADIUSLibraryLogger(),
	}
	return s
}

// dispatch は、受けたパケットの処理を goroutine に渡して、すぐにライブラリへ戻る（段階 2-6 の確認事項 M11）。
// layeh.com/radius の PacketServer は、同じ送信元と Identifier の要求を、ハンドラーが戻るまで黙って捨てる。
// ハンドラーは応答を送った後もログや後片付けで少し戻らないので、その間に届いた再送が Guard（重複応答の
// キャッシュ）に届かず、前の応答を送り直せなかった。すぐに戻ってライブラリの判定を効かなくし、重複は Guard だけで扱う。
// 同時に処理する数の上限は Guard が持つ。停止を始めた後のパケットは捨てる（NAS は送り直す）。
func (s *Server) dispatch(next radius.Handler) radius.Handler {
	return radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			return
		}
		s.inflight.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.inflight.Done()
			next.ServeRADIUS(w, r)
		}()
	})
}

// recoverHandler は、パケットの処理中のパニックを回復してログ（SYS_PANIC）に出す。
// layeh.com/radius の PacketServer はパケットごとの goroutine でハンドラーを呼び、パニックを回復しないので、
// 回復しないと 1 つのパケットでプロセスごと落ちる。そのパケットには応答しない（NAS は応答がなければ送り直す）。
func recoverHandler(next radius.Handler) radius.Handler {
	return radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("パケット処理中のパニック（回復して処理を続ける）",
					"event_id", "SYS_PANIC",
					"src_ip", ExtractIP(r.RemoteAddr),
					"code", r.Code,
					"panic", fmt.Sprint(p),
					"stack", string(debug.Stack()),
				)
			}
		}()
		next.ServeRADIUS(w, r)
	})
}

// ListenAndServe はUDPサーバーを起動する
func (s *Server) ListenAndServe() error {
	return s.ps.ListenAndServe()
}

// Shutdown はサーバーをグレースフルに停止する。新しいパケットを受けるのをやめ、処理中のパケット（dispatch で
// 渡したもの）が応答を送り終えるのを待ってから、待ち受けを閉じる（先に閉じると応答を送れないため）。
// ctx の期限が来たら、待つのをやめて閉じる。
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return s.ps.Shutdown(ctx)
}

// Serve は conn でパケットを受ける（試験で空きポートの接続を渡すときに使う）。
func (s *Server) Serve(conn net.PacketConn) error {
	return s.ps.Serve(conn)
}
