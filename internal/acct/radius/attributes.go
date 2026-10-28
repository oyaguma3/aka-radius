package radius

import (
	"encoding/binary"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
	"layeh.com/radius"
)

// RADIUS属性タイプ定数（RFC 2865/2866）
const (
	AttrTypeUserName        = 1
	AttrTypeNASIPAddress    = 4
	AttrTypeFramedIPAddr    = 8
	AttrTypeClass           = 25
	AttrTypeNASIdentifier   = 32
	AttrTypeProxyState      = 33
	AttrTypeAcctStatusType  = 40
	AttrTypeAcctInputOct    = 42
	AttrTypeAcctOutputOct   = 43
	AttrTypeAcctSessionID   = 44
	AttrTypeAcctSessionTime = 46
	AttrTypeCallingStation  = 31
	AttrTypeAcctInputGiga   = 52
	AttrTypeAcctOutputGiga  = 53
	AttrTypeEventTimestamp  = 55
)

// 属性抽出エラー
var (
	ErrMissingStatusType = errors.New("missing Acct-Status-Type")
	ErrMissingSessionID  = errors.New("missing Acct-Session-Id")
)

// ExtractAccountingAttributes はAccounting-Requestから必要な属性を抽出する。
func ExtractAccountingAttributes(packet *radius.Packet) (*AccountingAttributes, error) {
	attrs := &AccountingAttributes{}

	// Acct-Status-Type（必須）
	statusTypeAttr := packet.Get(radius.Type(AttrTypeAcctStatusType))
	if len(statusTypeAttr) < 4 {
		return nil, ErrMissingStatusType
	}
	attrs.AcctStatusType = binary.BigEndian.Uint32(statusTypeAttr)

	// Acct-Session-Id（Start/Stop/Interimでは必須、On/Offではオプション）
	sessionIDAttr := packet.Get(radius.Type(AttrTypeAcctSessionID))
	if len(sessionIDAttr) == 0 {
		if attrs.AcctStatusType != AcctStatusTypeOn && attrs.AcctStatusType != AcctStatusTypeOff {
			return nil, ErrMissingSessionID
		}
	} else {
		attrs.AcctSessionID = string(sessionIDAttr)
	}

	// Class（オプション - UUID抽出試行）
	classAttr := packet.Get(radius.Type(AttrTypeClass))
	if len(classAttr) > 0 {
		classValue := string(classAttr)
		if _, err := uuid.Parse(classValue); err == nil {
			attrs.ClassUUID = classValue
		}
	}

	// User-Name（オプション）
	userNameAttr := packet.Get(radius.Type(AttrTypeUserName))
	if len(userNameAttr) > 0 {
		attrs.UserName = string(userNameAttr)
	}

	// NAS-IP-Address
	nasIPAttr := packet.Get(radius.Type(AttrTypeNASIPAddress))
	if len(nasIPAttr) == 4 {
		attrs.NasIPAddress = net.IP(nasIPAttr).String()
	}

	// NAS-Identifier（オプション）
	nasIdentAttr := packet.Get(radius.Type(AttrTypeNASIdentifier))
	if len(nasIdentAttr) > 0 {
		attrs.NasIdentifier = string(nasIdentAttr)
	}

	// Framed-IP-Address
	framedIPAttr := packet.Get(radius.Type(AttrTypeFramedIPAddr))
	if len(framedIPAttr) == 4 {
		attrs.FramedIPAddress = net.IP(framedIPAttr).String()
	}

	// Acct-Input-Octets
	inputAttr := packet.Get(radius.Type(AttrTypeAcctInputOct))
	if len(inputAttr) >= 4 {
		attrs.InputOctets = binary.BigEndian.Uint32(inputAttr)
	}

	// Acct-Output-Octets
	outputAttr := packet.Get(radius.Type(AttrTypeAcctOutputOct))
	if len(outputAttr) >= 4 {
		attrs.OutputOctets = binary.BigEndian.Uint32(outputAttr)
	}

	// Acct-Session-Time
	timeAttr := packet.Get(radius.Type(AttrTypeAcctSessionTime))
	if len(timeAttr) >= 4 {
		attrs.SessionTime = binary.BigEndian.Uint32(timeAttr)
	}

	// Acct-Input/Output-Gigawords（RFC 2869。4 GiB で折り返した回数。MVP の設計概要 §10.2）
	if v := packet.Get(radius.Type(AttrTypeAcctInputGiga)); len(v) >= 4 {
		attrs.InputGigawords = binary.BigEndian.Uint32(v)
	}
	if v := packet.Get(radius.Type(AttrTypeAcctOutputGiga)); len(v) >= 4 {
		attrs.OutputGigawords = binary.BigEndian.Uint32(v)
	}

	// Event-Timestamp（RFC 2869。利用の集計の日の割り当てに使う）
	if v := packet.Get(radius.Type(AttrTypeEventTimestamp)); len(v) >= 4 {
		if sec := binary.BigEndian.Uint32(v); sec > 0 {
			attrs.EventTimestamp = time.Unix(int64(sec), 0)
		}
	}

	// Calling-Station-Id（オプション）
	if v := packet.Get(radius.Type(AttrTypeCallingStation)); len(v) > 0 {
		attrs.CallingStationID = string(v)
	}

	// Proxy-State（複数可）
	attrs.ProxyStates = extractProxyStatesRaw(packet)

	return attrs, nil
}

// extractProxyStatesRaw はパケットからProxy-State属性を直接抽出する
func extractProxyStatesRaw(packet *radius.Packet) [][]byte {
	var states [][]byte
	for _, attr := range packet.Attributes {
		if attr.Type == radius.Type(AttrTypeProxyState) {
			states = append(states, attr.Attribute)
		}
	}
	return states
}
