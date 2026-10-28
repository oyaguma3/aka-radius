package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_Errors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"引数なし", nil, 2, "使い方"},
		{"サーバーに引数", []string{"auth", "acct"}, 2, "引数をとりません"},
		{"不明なサブコマンド", []string{"gateway"}, 2, `不明なサブコマンド: "gateway"`},
		{"コマンドの誤り", []string{"client"}, 1, "usage: aka-radius client"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if code := run(tt.args, &buf); code != tt.code {
				t.Errorf("run(%v) = %d, want %d", tt.args, code, tt.code)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output %q does not contain %q", buf.String(), tt.want)
			}
		})
	}
}

func TestSubcommandNames(t *testing.T) {
	for _, name := range []string{"auth", "acct", "admin"} {
		if servers[name] == nil {
			t.Errorf("server %q is missing", name)
		}
	}
	for _, name := range []string{"client", "sources", "admin-cert", "fingerprint", "healthcheck"} {
		if commands[name] == nil {
			t.Errorf("command %q is missing", name)
		}
		if !strings.Contains(usage, name) {
			t.Errorf("usage does not mention %q", name)
		}
	}
}
