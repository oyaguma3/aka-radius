package processor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/aka-radius/internal/acct/radius"
	"github.com/oyaguma3/aka-radius/internal/acct/store"
	"github.com/oyaguma3/aka-radius/internal/usage"
)

type fakeRecorder struct {
	got []store.Record
	res store.Result
	err error
}

func (f *fakeRecorder) Account(_ context.Context, r store.Record) (store.Result, error) {
	f.got = append(f.got, r)
	return f.res, f.err
}

type fakeTrust struct {
	trusted bool
	err     error
}

func (f fakeTrust) TrustsStamp(context.Context, string) (bool, error) { return f.trusted, f.err }

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, usage.JST)

func newProc(rec *fakeRecorder, trust fakeTrust) *Processor {
	p := New(rec, trust, false)
	p.now = func() time.Time { return testNow }
	return p
}

func attrs(status uint32) *radius.AccountingAttributes {
	return &radius.AccountingAttributes{
		AcctStatusType: status, AcctSessionID: "acct-1", ClassUUID: "s1", UserName: "0440100123456789@realm",
		NasIdentifier: "ap-1", NasIPAddress: "10.0.0.1", FramedIPAddress: "10.1.0.5", CallingStationID: "aa-bb",
		InputOctets: 10, InputGigawords: 1, OutputOctets: 20, SessionTime: 30, Stamps: [][]byte{[]byte("42")},
	}
}

func TestProcess_BuildsRecord(t *testing.T) {
	rec := &fakeRecorder{res: store.Result{IMSI: "440100123456789", TenantID: 42, TenantSource: "stamp", SessionFound: true}}
	logs := captureLogs(t)
	if err := newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), attrs(radius.AcctStatusTypeInterim), "192.0.2.1", "t1"); err != nil {
		t.Fatal(err)
	}
	r := rec.got[0]
	want := store.Record{
		Kind: store.KindInterim, SeenKey: "acct:seen:192.0.2.1:ap-1:acct-1", SessionID: "s1",
		Seconds: 30, InOctets: 1<<32 + 10, OutOctets: 20, StampTenant: 42, UserNameIMSI: "440100123456789",
		Day: "20261028", Now: testNow, NasIP: "192.0.2.1", NasIdentifier: "ap-1", CallingStationID: "aa-bb",
		FramedIP: "10.1.0.5", AcctSessionID: "acct-1",
	}
	if r != want {
		t.Errorf("record = %+v\nwant     %+v", r, want)
	}
	if !strings.Contains(logs.String(), `"event_id":"ACCT_INTERIM"`) {
		t.Errorf("logs = %s", logs)
	}
}

func TestProcess_Stamps(t *testing.T) {
	tests := []struct {
		name   string
		stamps [][]byte
		trust  fakeTrust
		want   int64
		event  string
	}{
		{"信頼しない送信元", [][]byte{[]byte("42")}, fakeTrust{trusted: false}, 0, ""},
		{"印を読めない", [][]byte{[]byte("42")}, fakeTrust{err: errors.New("down")}, 0, "ACCT_STAMP_LOOKUP_ERR"},
		{"2 つ", [][]byte{[]byte("42"), []byte("7")}, fakeTrust{trusted: true}, 0, "ACCT_STAMP_MULTIPLE"},
		{"数でない", [][]byte{[]byte("x")}, fakeTrust{trusted: true}, 0, "ACCT_STAMP_INVALID"},
		{"刻印なし", nil, fakeTrust{trusted: true}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			rec := &fakeRecorder{res: store.Result{IMSI: "440100123456789", TenantSource: "none", SessionFound: true}}
			a := attrs(radius.AcctStatusTypeStart)
			a.Stamps = tt.stamps
			if err := newProc(rec, tt.trust).Process(context.Background(), a, "192.0.2.1", "t1"); err != nil {
				t.Fatal(err)
			}
			if rec.got[0].StampTenant != tt.want {
				t.Errorf("StampTenant = %d", rec.got[0].StampTenant)
			}
			if tt.event != "" && !strings.Contains(logs.String(), tt.event) {
				t.Errorf("logs = %s", logs)
			}
			if !strings.Contains(logs.String(), "ACCT_TENANT_UNKNOWN") {
				t.Errorf("no ACCT_TENANT_UNKNOWN: %s", logs)
			}
		})
	}
}

func TestProcess_ResultLogs(t *testing.T) {
	tests := []struct {
		name   string
		status uint32
		res    store.Result
		events []string
		absent []string
	}{
		{"Start の重複", radius.AcctStatusTypeStart, store.Result{Duplicate: true}, []string{"ACCT_DUPLICATE_START"}, []string{`"ACCT_START"`}},
		{"Interim の重複", radius.AcctStatusTypeInterim, store.Result{Duplicate: true}, []string{"ACCT_DUPLICATE_INTERIM"}, []string{`"ACCT_INTERIM"`}},
		{"Stop の重複", radius.AcctStatusTypeStop, store.Result{Duplicate: true}, []string{"ACCT_DUPLICATE_STOP"}, []string{`"ACCT_STOP"`}},
		{"順序の誤りと累計の減少", radius.AcctStatusTypeInterim,
			store.Result{IMSI: "440100123456789", TenantSource: "seen", Sequence: "no_start_received", CounterReset: true},
			[]string{"ACCT_SEQUENCE_ERR", "no_start_received", "USAGE_COUNTER_RESET", "ACCT_SESSION_NOT_FOUND", `"ACCT_INTERIM"`}, []string{"ACCT_TENANT_UNKNOWN"}},
		{"IMSI が分からない", radius.AcctStatusTypeStop, store.Result{TenantSource: "none", SessionFound: true},
			[]string{"ACCT_IMSI_UNKNOWN", `"ACCT_STOP"`}, []string{"ACCT_TENANT_UNKNOWN"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			rec := &fakeRecorder{res: tt.res}
			if err := newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), attrs(tt.status), "192.0.2.1", "t1"); err != nil {
				t.Fatal(err)
			}
			for _, e := range tt.events {
				if !strings.Contains(logs.String(), e) {
					t.Errorf("logs do not contain %s: %s", e, logs)
				}
			}
			for _, e := range tt.absent {
				if strings.Contains(logs.String(), e) {
					t.Errorf("logs contain %s: %s", e, logs)
				}
			}
		})
	}
}

func TestProcess_TimestampsAndErrors(t *testing.T) {
	// 7 日より前の Event-Timestamp は足し込まない（日を空にする）
	logs := captureLogs(t)
	rec := &fakeRecorder{res: store.Result{IMSI: "440100123456789", TenantSource: "stamp", SessionFound: true}}
	a := attrs(radius.AcctStatusTypeInterim)
	a.EventTimestamp = testNow.AddDate(0, 0, -10)
	if err := newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), a, "192.0.2.1", "t1"); err != nil {
		t.Fatal(err)
	}
	if rec.got[0].Day != "" || !strings.Contains(logs.String(), "ACCT_USAGE_TOO_OLD") {
		t.Errorf("day = %q logs = %s", rec.got[0].Day, logs)
	}
	// 未来の Event-Timestamp は受け取った時刻を使う
	a.EventTimestamp = testNow.Add(48 * time.Hour)
	_ = newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), a, "192.0.2.1", "t1")
	if rec.got[1].Day != "20261028" || !strings.Contains(logs.String(), "ACCT_EVENT_TIMESTAMP_FUTURE") {
		t.Errorf("day = %q", rec.got[1].Day)
	}
	// NAS-Identifier がなければ NAS-IP-Address で重複の判定のキーを作る
	a = attrs(radius.AcctStatusTypeStart)
	a.NasIdentifier = ""
	_ = newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), a, "192.0.2.1", "t1")
	if rec.got[2].SeenKey != "acct:seen:192.0.2.1:10.0.0.1:acct-1" {
		t.Errorf("seen key = %s", rec.got[2].SeenKey)
	}
	// 記録できなければ誤りを返す
	rec.err = errors.New("valkey down")
	if err := newProc(rec, fakeTrust{trusted: true}).Process(context.Background(), attrs(radius.AcctStatusTypeStart), "192.0.2.1", "t1"); err == nil {
		t.Error("error not returned")
	}
	// On / Off はログだけ
	n := len(rec.got)
	for _, st := range []uint32{radius.AcctStatusTypeOn, radius.AcctStatusTypeOff, 99} {
		if err := newProc(rec, fakeTrust{}).Process(context.Background(), &radius.AccountingAttributes{AcctStatusType: st}, "192.0.2.1", "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.got) != n || !strings.Contains(logs.String(), "ACCT_ON") || !strings.Contains(logs.String(), "ACCT_OFF") {
		t.Errorf("on/off recorded or not logged: %d", len(rec.got))
	}
}

func TestIMSIFromIdentity(t *testing.T) {
	for in, want := range map[string]string{
		"0440100123456789@realm": "440100123456789", "6440100123456789@realm": "440100123456789", "440100123456789": "440100123456789",
		"anonymous@realm": "", "2pseudonym@realm": "", "": "", "1440100123456789@realm": "",
	} {
		if got := IMSIFromIdentity(in); got != want {
			t.Errorf("IMSIFromIdentity(%q) = %q", in, got)
		}
	}
}
