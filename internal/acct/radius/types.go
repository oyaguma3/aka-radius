package radius

import "time"

// AccountingAttributes はAccounting-Requestから抽出された属性を表す
type AccountingAttributes struct {
	AcctStatusType  uint32   // Acct-Status-Type（1:Start, 2:Stop, 3:Interim, 7:On, 8:Off）
	AcctSessionID   string   // Acct-Session-Id（Start/Stop/Interimでは必須、On/Offではオプション）
	ClassUUID       string   // Class属性からパースしたUUID（空文字列の場合あり）
	UserName        string   // User-Name（オプション）
	NasIdentifier   string   // NAS-Identifier（オプション）
	NasIPAddress    string   // NAS-IP-Address
	FramedIPAddress string   // Framed-IP-Address
	InputOctets     uint32   // Acct-Input-Octets
	OutputOctets    uint32   // Acct-Output-Octets
	SessionTime     uint32   // Acct-Session-Time
	ProxyStates     [][]byte // Proxy-State属性（複数可）

	// InputGigawords・OutputGigawords は Acct-Input/Output-Gigawords（RFC 2869。2^32 オクテットの数）
	InputGigawords  uint32
	OutputGigawords uint32
	// EventTimestamp は Event-Timestamp（RFC 2869。なければ空の時刻）
	EventTimestamp time.Time
	// CallingStationID は Calling-Station-Id（端末の MAC アドレスなど）
	CallingStationID string
	// Stamps はテナントの刻印（Vendor-Specific の値。ハンドラーが入れる。信頼の印は processor が確かめる）
	Stamps [][]byte
}

// InputTotal は Gigawords を合わせた受信のオクテット数を返す。
func (a *AccountingAttributes) InputTotal() uint64 {
	return uint64(a.InputGigawords)<<32 | uint64(a.InputOctets)
}

// OutputTotal は Gigawords を合わせた送信のオクテット数を返す。
func (a *AccountingAttributes) OutputTotal() uint64 {
	return uint64(a.OutputGigawords)<<32 | uint64(a.OutputOctets)
}

// Acct-Status-Type値（RFC 2866）
const (
	AcctStatusTypeStart   uint32 = 1
	AcctStatusTypeStop    uint32 = 2
	AcctStatusTypeInterim uint32 = 3
	AcctStatusTypeOn      uint32 = 7
	AcctStatusTypeOff     uint32 = 8
)
