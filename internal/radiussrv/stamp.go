package radiussrv

import (
	"encoding/binary"
	"strconv"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
)

// VendorAttributes は Vendor-Specific（26）の中の、vendorID と vendorType の属性の値をすべて返す
// （RFC 2865 §5.26 の形。1 つの Vendor-Specific に複数の属性があってもよい）。テナントの刻印に使う（MVP の設計概要 §8.3）。
// 形の壊れた Vendor-Specific は、その後ろを読まない。auth と acct で共通。
func VendorAttributes(p *radius.Packet, vendorID uint32, vendorType byte) [][]byte {
	var out [][]byte
	for _, avp := range p.Attributes {
		if avp.Type != rfc2865.VendorSpecific_Type || len(avp.Attribute) < 4 {
			continue
		}
		if binary.BigEndian.Uint32(avp.Attribute[:4]) != vendorID {
			continue
		}
		for b := avp.Attribute[4:]; len(b) >= 2; {
			n := int(b[1])
			if n < 2 || n > len(b) {
				break
			}
			if b[0] == vendorType {
				out = append(out, b[2:n])
			}
			b = b[n:]
		}
	}
	return out
}

// ParseTenantStamp は刻印の値（テナントの ID の 10 進の文字列）を読む。10 進の数字だけで 1 以上でなければ false。
func ParseTenantStamp(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
