package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oyaguma3/aka-radius/internal/sessions"
	"github.com/oyaguma3/aka-radius/internal/usage"
)

// ---- セッション（段階 2-4 の J9） ----

// maxSessionIMSIs は imsi の絞り込みで受け付ける IMSI の数の上限。
const maxSessionIMSIs = 100

type sessionJSON struct {
	ID               string     `json:"id"`
	IMSI             string     `json:"imsi"`
	TenantID         int64      `json:"tenantId"`
	NasIP            string     `json:"nasIp"`
	NasIdentifier    string     `json:"nasIdentifier"`
	CallingStationID string     `json:"callingStationId,omitempty"`
	FramedIP         string     `json:"framedIp,omitempty"`
	AcctSessionID    string     `json:"acctSessionId,omitempty"`
	StartTime        *time.Time `json:"startTime,omitempty"`
	LastUpdate       *time.Time `json:"lastUpdate,omitempty"`
	Seconds          int64      `json:"seconds"`
	InOctets         int64      `json:"inOctets"`
	OutOctets        int64      `json:"outOctets"`
	ExpiresAt        time.Time  `json:"expiresAt"`
}

type sessionListJSON struct {
	Items      []sessionJSON `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func toSessionJSON(s sessions.Session) sessionJSON {
	return sessionJSON{
		ID: s.ID, IMSI: s.IMSI, TenantID: s.TenantID, NasIP: s.NasIP, NasIdentifier: s.NasIdentifier,
		CallingStationID: s.CallingStationID, FramedIP: s.FramedIP, AcctSessionID: s.AcctSessionID,
		StartTime: timePtr(s.StartTime), LastUpdate: timePtr(s.LastUpdate),
		Seconds: s.Seconds, InOctets: s.InOctets, OutOctets: s.OutOctets, ExpiresAt: s.ExpiresAt,
	}
}

var cursorPattern = regexp.MustCompile(`^[0-9]{1,9}$`)

// listSessions はセッションを、テナントか IMSI（カンマ区切り、100 件まで）で絞って返す（どちらか 1 つが必須）。
// 期限の早い順（最後に更新されたのが古い順）。
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantStr, imsiStr, cursor := q.Get("tenantId"), q.Get("imsi"), q.Get("cursor")
	switch {
	case (tenantStr == "") == (imsiStr == ""):
		badParams(causeInvalidQueryParam, invalidParam{"tenantId", "give exactly one of tenantId and imsi"}).write(w)
		return
	case cursor != "" && !cursorPattern.MatchString(cursor):
		badParams(causeInvalidQueryParam, invalidParam{"cursor", "must be a nextCursor value from a previous response"}).write(w)
		return
	}
	limit, p := queryInt(r, "limit", 100, 1, 1000)
	if p != nil {
		p.write(w)
		return
	}
	offset, _ := strconv.Atoi(cursor)
	now := time.Now()

	var items []sessions.Session
	var next int
	var err error
	if tenantStr != "" {
		tenant, perr := strconv.ParseInt(tenantStr, 10, 64)
		if perr != nil || tenant < 0 {
			badParams(causeInvalidQueryParam, invalidParam{"tenantId", "must be an integer of 0 or greater"}).write(w)
			return
		}
		items, next, err = h.Sessions.ListByTenant(r.Context(), tenant, offset, int(limit), now)
	} else {
		var imsis []string
		for imsi := range strings.SplitSeq(imsiStr, ",") {
			if !imsiPattern.MatchString(imsi) {
				badParams(causeInvalidQueryParam, invalidParam{"imsi", "must be 15 digits (comma separated)"}).write(w)
				return
			}
			imsis = append(imsis, imsi)
		}
		if len(imsis) > maxSessionIMSIs {
			badParams(causeInvalidQueryParam, invalidParam{"imsi", fmt.Sprintf("must be at most %d IMSIs", maxSessionIMSIs)}).write(w)
			return
		}
		items, next, err = h.Sessions.ListByIMSIs(r.Context(), imsis, offset, int(limit), now)
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := sessionListJSON{Items: make([]sessionJSON, len(items))}
	for i, s := range items {
		out.Items[i] = toSessionJSON(s)
	}
	if next >= 0 {
		out.NextCursor = strconv.Itoa(next)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 利用の集計（段階 2-4 の J10） ----

type usageJSON struct {
	Date      string `json:"date"`
	TenantID  int64  `json:"tenantId"`
	IMSI      string `json:"imsi"`
	AuthOK    int64  `json:"authOk"`
	Sessions  int64  `json:"sessions"`
	Seconds   int64  `json:"seconds"`
	InOctets  int64  `json:"inOctets"`
	OutOctets int64  `json:"outOctets"`
}

type usageListJSON struct {
	Items      []usageJSON `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

var usageCursorPattern = regexp.MustCompile(`^[0-9]{1,19}:[0-9]{15}$`)

// listUsage は日本時間の 1 日の利用の集計を、テナントと IMSI の辞書順に返す（その日が終わる前は途中の値）。
func (h *Handler) listUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date, cursor := q.Get("date"), q.Get("cursor")
	d, err := time.ParseInLocation("2006-01-02", date, usage.JST)
	if err != nil {
		badParams(causeInvalidQueryParam, invalidParam{"date", "is required as YYYY-MM-DD (Japan Standard Time)"}).write(w)
		return
	}
	if cursor != "" && !usageCursorPattern.MatchString(cursor) {
		badParams(causeInvalidQueryParam, invalidParam{"cursor", "must be a nextCursor value from a previous response"}).write(w)
		return
	}
	limit, p := queryInt(r, "limit", 1000, 1, 1000)
	if p != nil {
		p.write(w)
		return
	}
	records, next, err := h.Usage.List(r.Context(), usage.Day(d), cursor, int(limit))
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := usageListJSON{Items: make([]usageJSON, len(records)), NextCursor: next}
	for i, rec := range records {
		out.Items[i] = usageJSON{
			Date: date, TenantID: rec.TenantID, IMSI: rec.IMSI, AuthOK: rec.AuthOK, Sessions: rec.Sessions,
			Seconds: rec.Seconds, InOctets: rec.InOctets, OutOctets: rec.OutOctets,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
