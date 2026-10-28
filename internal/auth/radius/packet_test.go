package radius

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"
)

func TestGetEAPMessage_Single(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))
	eapData := []byte{0x01, 0x02, 0x00, 0x04}
	_ = rfc2869.EAPMessage_Set(p, eapData)

	got, ok := GetEAPMessage(p)
	if !ok {
		t.Fatal("GetEAPMessage returned false, want true")
	}
	if !bytes.Equal(got, eapData) {
		t.Errorf("GetEAPMessage = %x, want %x", got, eapData)
	}
}

func TestGetEAPMessage_Multiple(t *testing.T) {
	// 253バイト超のEAPメッセージを設定して結合取得を検証
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))
	eapData := make([]byte, 400)
	for i := range eapData {
		eapData[i] = byte(i % 256)
	}
	_ = rfc2869.EAPMessage_Set(p, eapData)

	got, ok := GetEAPMessage(p)
	if !ok {
		t.Fatal("GetEAPMessage returned false, want true")
	}
	if !bytes.Equal(got, eapData) {
		t.Errorf("GetEAPMessage returned %d bytes, want %d bytes", len(got), len(eapData))
	}
}

func TestGetEAPMessage_NotPresent(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	_, ok := GetEAPMessage(p)
	if ok {
		t.Error("GetEAPMessage returned true for packet without EAP-Message")
	}
}

func TestSplitEAPMessage_Short(t *testing.T) {
	data := make([]byte, 100)
	chunks := SplitEAPMessage(data)
	if len(chunks) != 1 {
		t.Errorf("SplitEAPMessage returned %d chunks, want 1", len(chunks))
	}
	if len(chunks[0]) != 100 {
		t.Errorf("chunk length = %d, want 100", len(chunks[0]))
	}
}

func TestSplitEAPMessage_Exact(t *testing.T) {
	data := make([]byte, 253)
	chunks := SplitEAPMessage(data)
	if len(chunks) != 1 {
		t.Errorf("SplitEAPMessage returned %d chunks, want 1", len(chunks))
	}
}

func TestSplitEAPMessage_Long(t *testing.T) {
	data := make([]byte, 510)
	chunks := SplitEAPMessage(data)
	if len(chunks) != 3 {
		t.Errorf("SplitEAPMessage returned %d chunks, want 3", len(chunks))
	}
	if len(chunks[0]) != 253 {
		t.Errorf("first chunk = %d bytes, want 253", len(chunks[0]))
	}
	if len(chunks[1]) != 253 {
		t.Errorf("second chunk = %d bytes, want 253", len(chunks[1]))
	}
	if len(chunks[2]) != 4 {
		t.Errorf("third chunk = %d bytes, want 4", len(chunks[2]))
	}
}

func TestSplitEAPMessage_Empty(t *testing.T) {
	chunks := SplitEAPMessage([]byte{})
	if len(chunks) != 1 {
		t.Errorf("SplitEAPMessage returned %d chunks, want 1", len(chunks))
	}
}

func TestSetEAPMessage_Roundtrip(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))
	eapData := make([]byte, 500)
	for i := range eapData {
		eapData[i] = byte(i % 256)
	}

	SetEAPMessage(p, eapData)

	got, ok := GetEAPMessage(p)
	if !ok {
		t.Fatal("GetEAPMessage returned false after SetEAPMessage")
	}
	if !bytes.Equal(got, eapData) {
		t.Error("SetEAPMessage → GetEAPMessage roundtrip failed")
	}
}

func TestGetState(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetState(p)
	if ok {
		t.Error("GetState returned true for empty packet")
	}

	// 属性あり
	state := []byte("test-state-value")
	_ = rfc2865.State_Set(p, state)

	got, ok := GetState(p)
	if !ok {
		t.Fatal("GetState returned false, want true")
	}
	if !bytes.Equal(got, state) {
		t.Errorf("GetState = %x, want %x", got, state)
	}
}

func TestSetState(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))
	state := []byte("my-state")
	SetState(p, state)

	got, ok := GetState(p)
	if !ok {
		t.Fatal("GetState returned false after SetState")
	}
	if !bytes.Equal(got, state) {
		t.Errorf("SetState roundtrip failed: got %x, want %x", got, state)
	}
}

func TestGetNASIdentifier(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetNASIdentifier(p)
	if ok {
		t.Error("GetNASIdentifier returned true for empty packet")
	}

	// 属性あり
	_ = rfc2865.NASIdentifier_AddString(p, "my-nas")
	got, ok := GetNASIdentifier(p)
	if !ok {
		t.Fatal("GetNASIdentifier returned false, want true")
	}
	if got != "my-nas" {
		t.Errorf("GetNASIdentifier = %q, want %q", got, "my-nas")
	}
}

func TestGetNASIPAddress(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetNASIPAddress(p)
	if ok {
		t.Error("GetNASIPAddress returned true for empty packet")
	}

	// 属性あり
	ip := net.ParseIP("192.168.1.1")
	_ = rfc2865.NASIPAddress_Set(p, ip)
	got, ok := GetNASIPAddress(p)
	if !ok {
		t.Fatal("GetNASIPAddress returned false, want true")
	}
	if !got.Equal(ip) {
		t.Errorf("GetNASIPAddress = %v, want %v", got, ip)
	}
}

func TestGetCalledStationID(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetCalledStationID(p)
	if ok {
		t.Error("GetCalledStationID returned true for empty packet")
	}

	// 属性あり
	_ = rfc2865.CalledStationID_AddString(p, "AA-BB-CC-DD-EE-FF:SSID")
	got, ok := GetCalledStationID(p)
	if !ok {
		t.Fatal("GetCalledStationID returned false, want true")
	}
	if got != "AA-BB-CC-DD-EE-FF:SSID" {
		t.Errorf("GetCalledStationID = %q, want %q", got, "AA-BB-CC-DD-EE-FF:SSID")
	}
}

func TestGetCallingStationID(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetCallingStationID(p)
	if ok {
		t.Error("GetCallingStationID returned true for empty packet")
	}

	// 属性あり
	_ = rfc2865.CallingStationID_AddString(p, "11-22-33-44-55-66")
	got, ok := GetCallingStationID(p)
	if !ok {
		t.Fatal("GetCallingStationID returned false, want true")
	}
	if got != "11-22-33-44-55-66" {
		t.Errorf("GetCallingStationID = %q, want %q", got, "11-22-33-44-55-66")
	}
}

func TestGetUserName(t *testing.T) {
	p := radius.New(radius.CodeAccessRequest, []byte("secret"))

	// 属性なし
	_, ok := GetUserName(p)
	if ok {
		t.Error("GetUserName returned true for empty packet")
	}

	// 属性あり
	_ = rfc2865.UserName_AddString(p, "0123456789012345@wlan.mnc001.mcc440.3gppnetwork.org")
	got, ok := GetUserName(p)
	if !ok {
		t.Fatal("GetUserName returned false, want true")
	}
	if got != "0123456789012345@wlan.mnc001.mcc440.3gppnetwork.org" {
		t.Errorf("GetUserName = %q", got)
	}
}

func TestGetVendorAttributes(t *testing.T) {
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

	got := GetVendorAttributes(p, 32473, 1)
	if len(got) != 2 || string(got[0]) != "42" || string(got[1]) != "7" {
		t.Fatalf("got %q", got)
	}
	if got := GetVendorAttributes(p, 32473, 3); len(got) != 0 {
		t.Errorf("type 3: %q", got)
	}
}
