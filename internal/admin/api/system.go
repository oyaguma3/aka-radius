package api

import (
	"encoding/json/jsontext"
	"net/http"
	"time"

	"github.com/oyaguma3/aka-radius/internal/admin/audit"
)

// ---- 状態 ----

type sourceStatusJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	TimeoutMs int64  `json:"timeoutMs"`
	// ClientCertificateNotAfter はクライアント証明書の期限（読めなければ返さない）
	ClientCertificateNotAfter *time.Time `json:"clientCertificateNotAfter,omitempty"`
	// SubscriberCount はこの取得先を指す加入者の数
	SubscriberCount int64 `json:"subscriberCount"`
}

type statusJSON struct {
	// Version は管理API の版、Build はビルドの版
	Version         string             `json:"version"`
	Build           string             `json:"build"`
	NodeName        string             `json:"nodeName"`
	StartedAt       time.Time          `json:"startedAt"`
	SubscriberCount int64              `json:"subscriberCount"`
	Sources         []sourceStatusJSON `json:"sources"`
	SourcesLoadedAt time.Time          `json:"sourcesLoadedAt"`
	// StampVendorID・StampVendorType はテナントの刻印の Vendor-Id と Vendor-Type（auth と同じ環境変数）
	StampVendorID   uint32   `json:"stampVendorId"`
	StampVendorType int      `json:"stampVendorType"`
	AdminClients    []string `json:"adminClients"`
}

// getStatus はノードの状態を返す。RADIUS クライアントの数・セッションの数・取得先の遮断器の状態は、
// それぞれの口と一緒に段階 2-4・2-5 で足す。
func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	count, err := h.Subs.Count(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	infos, loadedAt := h.Sources.Infos()
	ids := make([]string, len(infos))
	for i, in := range infos {
		ids[i] = in.ID
	}
	counts, err := h.Subs.SourceCounts(ctx, ids)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := statusJSON{
		Version:         Version,
		Build:           h.Build,
		NodeName:        h.NodeName,
		StartedAt:       h.StartedAt.UTC(),
		SubscriberCount: count,
		Sources:         make([]sourceStatusJSON, len(infos)),
		SourcesLoadedAt: loadedAt.UTC(),
		StampVendorID:   h.StampVendorID,
		StampVendorType: int(h.StampVendorType),
		AdminClients:    h.AdminClients,
	}
	for i, in := range infos {
		s := sourceStatusJSON{ID: in.ID, Name: in.Name, BaseURL: in.BaseURL, TimeoutMs: in.Timeout.Milliseconds(), SubscriberCount: counts[in.ID]}
		if !in.NotAfter.IsZero() {
			t := in.NotAfter.UTC()
			s.ClientCertificateNotAfter = &t
		}
		out.Sources[i] = s
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 監査ログ ----

type auditEntryJSON struct {
	ID         string         `json:"id"`
	Time       time.Time      `json:"time"`
	Operator   string         `json:"operator"`
	MgmtClient string         `json:"mgmtClient"`
	Action     string         `json:"action"`
	Target     string         `json:"target"`
	TraceID    string         `json:"traceId"`
	Detail     jsontext.Value `json:"detail,omitempty"`
}

type auditListJSON struct {
	Items      []auditEntryJSON `json:"items"`
	NextBefore string           `json:"nextBefore,omitempty"`
}

// listAuditLogs は監査ログを新しい順に返す。続きは nextBefore を次の before に渡す。
func (h *Handler) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if before != "" && !audit.StreamIDPattern.MatchString(before) {
		badParams(causeInvalidQueryParam, invalidParam{"before", "must be a nextBefore value from a previous response"}).write(w)
		return
	}
	limit, p := queryInt(r, "limit", 100, 1, 500)
	if p != nil {
		p.write(w)
		return
	}
	entries, next, err := h.Audit.List(r.Context(), before, int(limit))
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := auditListJSON{Items: make([]auditEntryJSON, len(entries)), NextBefore: next}
	for i, e := range entries {
		out.Items[i] = auditEntryJSON{
			ID: e.ID, Time: e.Time, Operator: e.Operator, MgmtClient: e.MgmtClient,
			Action: e.Action, Target: e.Target, TraceID: e.TraceID,
		}
		if e.Detail != "" {
			out.Items[i].Detail = jsontext.Value(e.Detail)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
