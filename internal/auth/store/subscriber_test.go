package store

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestSubscriberStore_GetSourceID(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.HSet("sub:001010000000001", "source_id", "vecgen")
	mr.HSet("sub:001010000000002", "ki", "x") // source_id のない加入者

	vc, err := NewValkeyClient(newTestConfig(mr.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Close()
	s := NewSubscriberStore(vc)

	tests := []struct{ imsi, want string }{
		{"001010000000001", "vecgen"},
		{"001010000000002", ""},
		{"001010000000003", ""}, // 加入者がいない
	}
	for _, tt := range tests {
		got, err := s.GetSourceID(context.Background(), tt.imsi)
		if err != nil || got != tt.want {
			t.Errorf("GetSourceID(%s) = %q, %v; want %q, nil", tt.imsi, got, err, tt.want)
		}
	}

	mr.Close()
	if _, err := s.GetSourceID(context.Background(), "001010000000001"); !errors.Is(err, ErrValkeyUnavailable) {
		t.Errorf("GetSourceID(valkey down) error = %v, want ErrValkeyUnavailable", err)
	}
}
