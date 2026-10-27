package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, name := range []string{"auth", "acct", "admin"} {
		var buf bytes.Buffer
		if run, ok := lookup([]string{name}, &buf); !ok || run == nil {
			t.Errorf("lookup(%q) = %v, want the entry point", name, ok)
		}
		if buf.Len() != 0 {
			t.Errorf("lookup(%q) wrote %q", name, buf.String())
		}
	}
}

func TestLookup_Errors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"引数なし", nil, "使い方"},
		{"引数が 2 つ", []string{"auth", "acct"}, "使い方"},
		{"不明なサブコマンド", []string{"gateway"}, `不明なサブコマンド: "gateway"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, ok := lookup(tt.args, &buf); ok {
				t.Fatal("lookup returned ok")
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output %q does not contain %q", buf.String(), tt.want)
			}
		})
	}
}
