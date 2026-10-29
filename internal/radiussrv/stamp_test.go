package radiussrv

import (
	"encoding/binary"
	"testing"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
)

func TestVendorAttributes(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("s"))
	vsa := func(vendor uint32, subs ...[]byte) radius.Attribute {
		b := binary.BigEndian.AppendUint32(nil, vendor)
		for _, s := range subs {
			b = append(b, s...)
		}
		return b
	}
	sub := func(typ byte, val string) []byte { return append([]byte{typ, byte(len(val) + 2)}, val...) }
	p.Add(rfc2865.VendorSpecific_Type, vsa(32473, sub(1, "42"), sub(2, "x")))
	p.Add(rfc2865.VendorSpecific_Type, vsa(9, sub(1, "cisco")))          // 別の Vendor-Id
	p.Add(rfc2865.VendorSpecific_Type, vsa(32473, sub(1, "7")))          // 2 つ目の刻印
	p.Add(rfc2865.VendorSpecific_Type, vsa(32473, []byte{1, 0xff, 'a'})) // 長さが壊れている
	p.Add(rfc2865.VendorSpecific_Type, radius.Attribute{0, 0})           // Vendor-Id が短い

	got := VendorAttributes(p, 32473, 1)
	if len(got) != 2 || string(got[0]) != "42" || string(got[1]) != "7" {
		t.Fatalf("got %q", got)
	}
	if got := VendorAttributes(p, 32473, 3); len(got) != 0 {
		t.Errorf("type 3: %q", got)
	}
}

func TestParseTenantStamp(t *testing.T) {
	for in, want := range map[string]int64{"42": 42, "1": 1, "0": 0, "": 0, "+1": 0, "-1": 0, "4 2": 0, "99999999999999999999": 0} {
		got, ok := ParseTenantStamp([]byte(in))
		if got != want || ok != (want > 0) {
			t.Errorf("ParseTenantStamp(%q) = %d %v", in, got, ok)
		}
	}
}
