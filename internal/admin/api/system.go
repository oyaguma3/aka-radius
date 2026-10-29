package api

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"time"

	"github.com/oyaguma3/aka-radius/internal/audit"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
)

// ---- 状態 ----

// breakerJSON は auth が報告した遮断器の状態（報告がまだないか、その取得先が報告にないときは返さない）。
type breakerJSON struct {
	State      string    `json:"state"`
	Since      time.Time `json:"since"`
	ReportedAt time.Time `json:"reportedAt"`
}

type sourceStatusJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	TimeoutMs int64  `json:"timeoutMs"`
	// ClientCertificateNotAfter はクライアント証明書の期限（読めなければ返さない）
	ClientCertificateNotAfter *time.Time `json:"clientCertificateNotAfter,omitempty"`
	// SubscriberCount はこの取得先を指す加入者の数
	SubscriberCount int64        `json:"subscriberCount"`
	CircuitBreaker  *breakerJSON `json:"circuitBreaker,omitempty"`
}

type statusJSON struct {
	// Version は管理API の版、Build はビルドの版
	Version         string    `json:"version"`
	Build           string    `json:"build"`
	NodeName        string    `json:"nodeName"`
	StartedAt       time.Time `json:"startedAt"`
	SubscriberCount int64     `json:"subscriberCount"`
	// ClientCount は RADIUS クライアントの数
	ClientCount int64 `json:"clientCount"`
	// SessionCount・SessionsCountedAt は acct が 1 時間ごとの掃除で数えたセッションの数と、数えた時刻（まだ数えていなければ返さない）
	SessionCount      *int64             `json:"sessionCount,omitempty"`
	SessionsCountedAt *time.Time         `json:"sessionsCountedAt,omitempty"`
	Sources           []sourceStatusJSON `json:"sources"`
	SourcesLoadedAt   time.Time          `json:"sourcesLoadedAt"`
	// StampVendorID・StampVendorType はテナントの刻印の Vendor-Id と Vendor-Type（auth と同じ環境変数）
	StampVendorID   uint32 `json:"stampVendorId"`
	StampVendorType int    `json:"stampVendorType"`
	// AdminClients・AdminClientsLoadedAt は管理クライアントの識別名と、ピンのファイルを読んだ時刻
	AdminClients         []string  `json:"adminClients"`
	AdminClientsLoadedAt time.Time `json:"adminClientsLoadedAt"`
}

// sourceCounts は取得先ごとの加入者の数と、遮断器の状態の報告を読む（/status と /sources で共通）。
func (h *Handler) sourceCounts(ctx context.Context, infos []source.Info) (map[string]int64, source.Status, bool, error) {
	ids := make([]string, len(infos))
	for i, in := range infos {
		ids[i] = in.ID
	}
	counts, err := h.Subs.SourceCounts(ctx, ids)
	if err != nil {
		return nil, source.Status{}, false, err
	}
	st, reported, err := h.SourceStatus.Read(ctx)
	if err != nil {
		return nil, source.Status{}, false, err
	}
	return counts, st, reported, nil
}

func breakerOf(st source.Status, reported bool, id string) *breakerJSON {
	b, ok := st.Sources[id]
	if !reported || !ok {
		return nil
	}
	return &breakerJSON{State: b.State, Since: b.Since.UTC(), ReportedAt: st.ReportedAt.UTC()}
}

// getStatus はノードの状態を返す。
func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	count, err := h.Subs.Count(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	clientCount, err := h.Clients.Count(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	infos, loadedAt := h.Sources.Infos()
	counts, st, reported, err := h.sourceCounts(ctx, infos)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	sessCount, countedAt, counted, err := h.Sessions.Stats(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	adminClients, adminLoadedAt := h.AdminClients()
	out := statusJSON{
		Version:              Version,
		Build:                h.Build,
		NodeName:             h.NodeName,
		StartedAt:            h.StartedAt.UTC(),
		SubscriberCount:      count,
		ClientCount:          clientCount,
		Sources:              make([]sourceStatusJSON, len(infos)),
		SourcesLoadedAt:      loadedAt.UTC(),
		StampVendorID:        h.StampVendorID,
		StampVendorType:      int(h.StampVendorType),
		AdminClients:         adminClients,
		AdminClientsLoadedAt: adminLoadedAt.UTC(),
	}
	if counted {
		at := countedAt.UTC()
		out.SessionCount, out.SessionsCountedAt = &sessCount, &at
	}
	for i, in := range infos {
		out.Sources[i] = sourceStatusJSON{
			ID: in.ID, Name: in.Name, BaseURL: in.BaseURL, TimeoutMs: in.Timeout.Milliseconds(),
			ClientCertificateNotAfter: timePtr(in.NotAfter.UTC()),
			SubscriberCount:           counts[in.ID],
			CircuitBreaker:            breakerOf(st, reported, in.ID),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- RADIUS クライアント ----

type clientJSON struct {
	IP         string    `json:"ip"`
	Name       string    `json:"name"`
	TrustStamp bool      `json:"trustStamp"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type clientListJSON struct {
	Items []clientJSON `json:"items"`
}

// listClients は RADIUS クライアントを IP の順に返す（共有シークレットは返さない。書き込みはコマンドだけ）。
func (h *Handler) listClients(w http.ResponseWriter, r *http.Request) {
	list, err := h.Clients.List(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := clientListJSON{Items: make([]clientJSON, len(list))}
	for i, c := range list {
		out.Items[i] = clientJSON{IP: c.IP, Name: c.Name, TrustStamp: c.TrustStamp, CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC()}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- 取得先の登録簿 ----

type certJSON struct {
	Subject  string     `json:"subject"`
	NotAfter *time.Time `json:"notAfter,omitempty"`
	// Fingerprint は SHA-256 のフィンガープリント（クライアント証明書だけ）
	Fingerprint string `json:"fingerprint,omitempty"`
}

type sourceJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	TimeoutMs int64  `json:"timeoutMs"`
	// ClientCertificate はクライアント証明書（読めなければ返さない）、ServerCAs は取得先のサーバーの CA
	ClientCertificate *certJSON    `json:"clientCertificate,omitempty"`
	ServerCAs         []certJSON   `json:"serverCas"`
	SubscriberCount   int64        `json:"subscriberCount"`
	CircuitBreaker    *breakerJSON `json:"circuitBreaker,omitempty"`
}

type sourceListJSON struct {
	Items    []sourceJSON `json:"items"`
	LoadedAt time.Time    `json:"loadedAt"`
}

// listSources は取得先の登録簿を、ファイルの順に返す（鍵とファイルのパスは返さない）。
func (h *Handler) listSources(w http.ResponseWriter, r *http.Request) {
	infos, loadedAt := h.Sources.Infos()
	counts, st, reported, err := h.sourceCounts(r.Context(), infos)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := sourceListJSON{Items: make([]sourceJSON, len(infos)), LoadedAt: loadedAt.UTC()}
	for i, in := range infos {
		s := sourceJSON{
			ID: in.ID, Name: in.Name, BaseURL: in.BaseURL, TimeoutMs: in.Timeout.Milliseconds(),
			ServerCAs:       make([]certJSON, len(in.ServerCAs)),
			SubscriberCount: counts[in.ID],
			CircuitBreaker:  breakerOf(st, reported, in.ID),
		}
		if in.ClientCertFingerprint != "" {
			s.ClientCertificate = &certJSON{Subject: in.ClientCertSubject, NotAfter: timePtr(in.NotAfter.UTC()), Fingerprint: in.ClientCertFingerprint}
		}
		for j, ca := range in.ServerCAs {
			s.ServerCAs[j] = certJSON{Subject: ca.Subject, NotAfter: timePtr(ca.NotAfter.UTC())}
		}
		out.Items[i] = s
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
