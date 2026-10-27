package radius

import (
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
	"layeh.com/radius"
)

// BuildAccountingResponse はAccounting-Responseパケットを生成する（RFC 2866）。
// Proxy-Stateエコーバックを行う。
// Response Authenticatorはgo-radiusライブラリのEncode()が自動計算する。
func BuildAccountingResponse(request *radius.Packet, proxyStates [][]byte) *radius.Packet {
	response := request.Response(radius.CodeAccountingResponse)

	// Proxy-Stateエコーバック
	(&radiussrv.ProxyStates{Values: proxyStates}).Apply(response)

	return response
}
