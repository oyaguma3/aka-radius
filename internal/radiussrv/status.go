package radiussrv

import (
	"log/slog"

	"layeh.com/radius"
)

// HandleStatusServer はStatus-Server(Code=12)を処理し、respCode の応答を返す。
// RFC 5997準拠のヘルスチェック応答。認証の口は Access-Accept（D-09 §5.7）、Accounting の口は
// Accounting-Response（D-10 §4.3）を返す。
// Message-Authenticator検証失敗時は failEventID のWARNログを出してnilを返す（応答なし）。
func HandleStatusServer(request *radius.Packet, secret []byte, respCode radius.Code, failEventID, srcIP, traceID string) *radius.Packet {
	// 1. Message-Authenticator検証
	if !VerifyMessageAuthenticator(request, secret) {
		slog.Warn("Status-Server: Message-Authenticator検証失敗",
			"event_id", failEventID,
			"trace_id", traceID,
			"src_ip", srcIP,
		)
		return nil
	}

	// 2. 応答を作成（EAP-Messageなし。Response Authenticatorはライブラリの Encode() が計算する）
	resp := request.Response(respCode)

	// 3. Proxy-Stateコピー
	ExtractProxyStates(request).Apply(resp)

	// 4. Message-Authenticator生成
	SetMessageAuthenticator(resp, secret, request.Authenticator)

	// 定期的に届くヘルスチェックのため、正常応答のログはDEBUGとする（失敗はWARNのまま）
	slog.Debug("Status-Server: 応答送信",
		"event_id", "RADIUS_STATUS_OK",
		"trace_id", traceID,
		"src_ip", srcIP,
	)

	return resp
}
