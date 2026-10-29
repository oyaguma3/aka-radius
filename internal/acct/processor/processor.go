// Package processor は Accounting-Request を処理する（MVP の設計概要 §10。段階 2-4 で作り直した）。
// 重複の判定・前回の累計からの増分・利用の集計・セッションの更新は、store.RecordStore の 1 つの Lua で行う。
package processor

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/oyaguma3/aka-radius/internal/acct/radius"
	"github.com/oyaguma3/aka-radius/internal/acct/store"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/radiussrv"
	"github.com/oyaguma3/aka-radius/internal/usage"
)

// Recorder は Accounting を記録する（store.RecordStore）。
type Recorder interface {
	Account(ctx context.Context, r store.Record) (store.Result, error)
}

// StampTrust は、送信元の RADIUS クライアントの刻印を使ってよいかを引く（radiussrv.StampTrust）。
type StampTrust interface {
	TrustsStamp(ctx context.Context, ip string) (bool, error)
}

// Processor は Accounting-Request を処理する。
type Processor struct {
	rec      Recorder
	trust    StampTrust
	maskIMSI bool
	// now は現在時刻を返す（試験用）。
	now func() time.Time
}

// New は Processor を作る。
func New(rec Recorder, trust StampTrust, maskIMSI bool) *Processor {
	return &Processor{rec: rec, trust: trust, maskIMSI: maskIMSI, now: time.Now}
}

// Process は Accounting-Request を 1 つ処理する。誤り（Valkey に書けない）を返したら、呼び出し側は応答しない
// （NAS に送り直させて、利用の集計の取りこぼしを防ぐ）。
func (p *Processor) Process(ctx context.Context, a *radius.AccountingAttributes, srcIP, traceID string) error {
	log := slog.With("trace_id", traceID, "src_ip", srcIP, "nas_identifier", a.NasIdentifier)
	var kind store.Kind
	switch a.AcctStatusType {
	case radius.AcctStatusTypeStart:
		kind = store.KindStart
	case radius.AcctStatusTypeInterim:
		kind = store.KindInterim
	case radius.AcctStatusTypeStop:
		kind = store.KindStop
	case radius.AcctStatusTypeOn, radius.AcctStatusTypeOff:
		// その NAS のセッションは期限で消える（本PoC と同じくログだけ。段階 2-4 の J12）
		event, msg := "ACCT_ON", "accounting on"
		if a.AcctStatusType == radius.AcctStatusTypeOff {
			event, msg = "ACCT_OFF", "accounting off"
		}
		log.Info(msg, "event_id", event, "nas_ip_address", a.NasIPAddress)
		return nil
	default:
		return nil
	}

	now := p.now()
	place := usage.Place(a.EventTimestamp, now)
	if place.Future {
		log.Warn("Event-Timestamp is in the future (using the received time)",
			"event_id", "ACCT_EVENT_TIMESTAMP_FUTURE", "event_timestamp", a.EventTimestamp.Unix())
	}
	nas := a.NasIdentifier
	if nas == "" {
		nas = a.NasIPAddress
	}
	rec := store.Record{
		Kind:             kind,
		SeenKey:          store.SeenKey(srcIP, nas, a.AcctSessionID),
		SessionID:        a.ClassUUID,
		Seconds:          uint64(a.SessionTime),
		InOctets:         a.InputTotal(),
		OutOctets:        a.OutputTotal(),
		StampTenant:      p.stampTenant(ctx, log, a, srcIP),
		UserNameIMSI:     IMSIFromIdentity(a.UserName),
		Day:              place.Day,
		Now:              now,
		NasIP:            srcIP,
		NasIdentifier:    a.NasIdentifier,
		CallingStationID: a.CallingStationID,
		FramedIP:         a.FramedIPAddress,
		AcctSessionID:    a.AcctSessionID,
	}
	res, err := p.rec.Account(ctx, rec)
	if err != nil {
		log.Error("failed to record accounting (no response, the NAS will retransmit)",
			"event_id", "VALKEY_CONN_ERR", "acct_session_id", a.AcctSessionID, "error", err.Error())
		return err
	}
	p.logResult(log, kind, a, res, place)
	return nil
}

// stampTenant は信頼する送信元の刻印のテナントを返す（なければ 0）。刻印が 2 つ以上・数でない場合は使わない。
func (p *Processor) stampTenant(ctx context.Context, log *slog.Logger, a *radius.AccountingAttributes, srcIP string) int64 {
	if len(a.Stamps) == 0 {
		return 0
	}
	trusted, err := p.trust.TrustsStamp(ctx, srcIP)
	if err != nil {
		log.Error("failed to read the stamp trust of the RADIUS client", "event_id", "ACCT_STAMP_LOOKUP_ERR", "error", err.Error())
		return 0
	}
	if !trusted {
		return 0
	}
	if len(a.Stamps) > 1 {
		log.Warn("multiple tenant stamps (not used)", "event_id", "ACCT_STAMP_MULTIPLE", "count", len(a.Stamps))
		return 0
	}
	tenant, ok := radiussrv.ParseTenantStamp(a.Stamps[0])
	if !ok {
		log.Warn("tenant stamp is not a decimal number (not used)", "event_id", "ACCT_STAMP_INVALID")
		return 0
	}
	return tenant
}

// logResult は記録の結果をログに出す。
func (p *Processor) logResult(log *slog.Logger, kind store.Kind, a *radius.AccountingAttributes, res store.Result, place usage.Placement) {
	log = log.With("acct_session_id", a.AcctSessionID)
	if res.Duplicate {
		switch kind {
		case store.KindStart:
			log.Warn("duplicate accounting start", "event_id", "ACCT_DUPLICATE_START")
		case store.KindInterim:
			log.Warn("duplicate accounting interim", "event_id", "ACCT_DUPLICATE_INTERIM")
		default:
			log.Debug("duplicate accounting stop", "event_id", "ACCT_DUPLICATE_STOP")
		}
		return
	}
	imsi := logging.MaskIMSI(res.IMSI, p.maskIMSI)
	if res.Sequence != "" {
		log.Warn("accounting sequence error", "event_id", "ACCT_SEQUENCE_ERR", "reason", res.Sequence, "imsi", imsi)
	}
	if !res.SessionFound {
		log.Warn("session not found (usage only)", "event_id", "ACCT_SESSION_NOT_FOUND", "class_uuid", a.ClassUUID, "imsi", imsi)
	}
	if res.CounterReset {
		log.Warn("accounting counters decreased (counted the new values as the increment)", "event_id", "USAGE_COUNTER_RESET", "imsi", imsi)
	}
	switch {
	case res.IMSI == "":
		log.Warn("IMSI is unknown (usage not counted)", "event_id", "ACCT_IMSI_UNKNOWN", "user_name", logging.MaskUserName(a.UserName, p.maskIMSI))
	case place.TooOld:
		log.Warn("accounting is older than the usage retention (usage not counted)",
			"event_id", "ACCT_USAGE_TOO_OLD", "imsi", imsi, "event_timestamp", a.EventTimestamp.Unix())
	case res.TenantSource == "none":
		log.Warn("tenant is unknown (counted as tenant 0)", "event_id", "ACCT_TENANT_UNKNOWN", "imsi", imsi)
	}
	event, msg := "ACCT_INTERIM", "accounting interim"
	switch kind {
	case store.KindStart:
		event, msg = "ACCT_START", "accounting start"
	case store.KindStop:
		event, msg = "ACCT_STOP", "accounting stop"
	}
	log.Info(msg,
		"event_id", event,
		"imsi", imsi,
		"tenant_id", res.TenantID,
		"tenant_source", res.TenantSource,
		"session_id", a.ClassUUID,
		"day", place.Day,
		"delta_seconds", res.Seconds,
		"delta_in_octets", res.InOctets,
		"delta_out_octets", res.OutOctets,
	)
}

var imsiPattern = regexp.MustCompile(`^[0-9]{15}$`)

// IMSIFromIdentity は User-Name（EAP の永続 ID の形 0<IMSI>@realm・6<IMSI>@realm、または 15 桁）から IMSI を取り出す。
// 匿名の外側の名前や仮名なら空を返す。
func IMSIFromIdentity(identity string) string {
	user, _, _ := strings.Cut(identity, "@")
	if len(user) == 16 && (user[0] == '0' || user[0] == '6') && imsiPattern.MatchString(user[1:]) {
		return user[1:]
	}
	if imsiPattern.MatchString(user) {
		return user
	}
	return ""
}
