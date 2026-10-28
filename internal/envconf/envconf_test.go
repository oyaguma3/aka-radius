package envconf

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCommon(t *testing.T) {
	t.Setenv("RADIUS_VALKEY_HOST", "localhost")
	t.Setenv("RADIUS_VALKEY_PORT", "16379")
	t.Setenv("RADIUS_VALKEY_PASSWORD", "secret")
	t.Setenv("RADIUS_LOG_LEVEL", "DEBUG")
	t.Setenv("RADIUS_LOG_MASK_IMSI", "true")

	c, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon() returned error: %v", err)
	}
	want := Common{ValkeyHost: "localhost", ValkeyPort: "16379", ValkeyPassword: "secret", LogLevel: "DEBUG", LogMaskIMSI: true}
	if c != want {
		t.Errorf("LoadCommon() = %+v, want %+v", c, want)
	}
	if got := c.ValkeyAddr(); got != "localhost:16379" {
		t.Errorf("ValkeyAddr() = %q, want %q", got, "localhost:16379")
	}
}

func TestLoadCommonDefaults(t *testing.T) {
	t.Setenv("RADIUS_VALKEY_PASSWORD", "secret")

	c, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon() returned error: %v", err)
	}
	want := Common{ValkeyHost: "valkey", ValkeyPort: "6379", ValkeyPassword: "secret", LogLevel: "INFO", LogMaskIMSI: false}
	if c != want {
		t.Errorf("LoadCommon() = %+v, want %+v", c, want)
	}
}

func TestLoadCommonErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"パスワードがない", map[string]string{}, []string{"RADIUS_VALKEY_PASSWORD"}},
		{"真偽値が誤り", map[string]string{"RADIUS_VALKEY_PASSWORD": "x", "RADIUS_LOG_MASK_IMSI": "yes"}, []string{"RADIUS_LOG_MASK_IMSI"}},
		{"2 つとも誤り", map[string]string{"RADIUS_LOG_MASK_IMSI": "yes"}, []string{"RADIUS_VALKEY_PASSWORD", "RADIUS_LOG_MASK_IMSI"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := LoadCommon()
			if err == nil {
				t.Fatal("LoadCommon() returned nil, want error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %s", err, w)
				}
			}
		})
	}
}

func TestBool(t *testing.T) {
	t.Setenv("X_BOOL", "")
	if b, err := Bool("X_BOOL", true); err != nil || !b {
		t.Errorf("Bool(empty, true) = %v, %v", b, err)
	}
	t.Setenv("X_BOOL", "false")
	if b, err := Bool("X_BOOL", true); err != nil || b {
		t.Errorf("Bool(false) = %v, %v", b, err)
	}
}

func TestPositiveInt(t *testing.T) {
	tests := []struct {
		v       string
		want    int64
		wantErr bool
	}{
		{"", 10, false},
		{"5", 5, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		t.Setenv("X_INT", tt.v)
		got, err := PositiveInt("X_INT", 10)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("PositiveInt(%q) = %d, %v; want %d, err=%v", tt.v, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRequired(t *testing.T) {
	t.Setenv("X_REQ", " ")
	if _, err := Required("X_REQ"); err == nil {
		t.Error("Required(blank) returned nil error")
	}
	t.Setenv("X_REQ", "v")
	if v, err := Required("X_REQ"); err != nil || v != "v" {
		t.Errorf("Required(v) = %q, %v", v, err)
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("X_DUR", "")
	if d, err := Duration("X_DUR", 4*time.Second, time.Second, time.Minute); err != nil || d != 4*time.Second {
		t.Errorf("default: %v %v", d, err)
	}
	t.Setenv("X_DUR", "1500ms")
	if d, err := Duration("X_DUR", 4*time.Second, time.Second, time.Minute); err != nil || d != 1500*time.Millisecond {
		t.Errorf("set: %v %v", d, err)
	}
	for _, bad := range []string{"abc", "100ms", "2m"} {
		t.Setenv("X_DUR", bad)
		if _, err := Duration("X_DUR", 4*time.Second, time.Second, time.Minute); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestStampVendor(t *testing.T) {
	t.Setenv("RADIUS_STAMP_VENDOR_ID", "")
	t.Setenv("RADIUS_STAMP_VENDOR_TYPE", "")
	if id, typ, err := StampVendor(); err != nil || id != 32473 || typ != 1 {
		t.Errorf("default: %d %d %v", id, typ, err)
	}
	t.Setenv("RADIUS_STAMP_VENDOR_ID", "9")
	t.Setenv("RADIUS_STAMP_VENDOR_TYPE", "2")
	if id, typ, err := StampVendor(); err != nil || id != 9 || typ != 2 {
		t.Errorf("set: %d %d %v", id, typ, err)
	}
	t.Setenv("RADIUS_STAMP_VENDOR_ID", "4294967296")
	if _, _, err := StampVendor(); err == nil {
		t.Error("too large vendor id accepted")
	}
	t.Setenv("RADIUS_STAMP_VENDOR_ID", "9")
	t.Setenv("RADIUS_STAMP_VENDOR_TYPE", "256")
	if _, _, err := StampVendor(); err == nil {
		t.Error("too large vendor type accepted")
	}
}
