package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
)

var (
	// imsiPattern は IMSI の形（本PoC と同じ 15 桁）。
	imsiPattern       = regexp.MustCompile(`^[0-9]{15}$`)
	imsiPrefixPattern = regexp.MustCompile(`^[0-9]{1,15}$`)
	// sourceIDPattern は取得先の ID の形（取得先の登録簿と同じ）。
	sourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// ルールの値の範囲（MVP の設計概要 §6.2・§8.2。長さと上限は本PoC と同じ）。
const (
	maxRules          = 32
	maxNasIDLen       = 253
	maxSSIDs          = 32
	maxSSIDLen        = 32
	maxVlanID         = 4094
	maxSessionTimeout = 86400
)

type subscriberJSON struct {
	IMSI           string            `json:"imsi"`
	State          string            `json:"state"`
	StateChangedAt time.Time         `json:"stateChangedAt"`
	SourceID       string            `json:"sourceId"`
	Rules          []subscriber.Rule `json:"rules"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// ruleJSON は要求のルール（項目の有無を見分けるためにポインター）。
type ruleJSON struct {
	TenantID       *int64    `json:"tenantId"`
	NasID          *string   `json:"nasId"`
	SSIDs          *[]string `json:"ssids"`
	VlanID         *int      `json:"vlanId"`
	SessionTimeout *int      `json:"sessionTimeout"`
}

type subscriberCreateJSON struct {
	IMSI     *string     `json:"imsi"`
	State    *string     `json:"state"`
	SourceID *string     `json:"sourceId"`
	Rules    *[]ruleJSON `json:"rules"`
}

type stateChangeJSON struct {
	State  *string `json:"state"`
	Reason *string `json:"reason"`
}

type reinstateJSON struct {
	Reason *string `json:"reason"`
}

type subscriberListJSON struct {
	Items      []subscriberJSON `json:"items"`
	Total      int64            `json:"total"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func toSubscriberJSON(sub subscriber.Subscriber) subscriberJSON {
	rules := sub.Rules
	if rules == nil {
		rules = []subscriber.Rule{}
	}
	return subscriberJSON{
		IMSI:           sub.IMSI,
		State:          string(sub.State),
		StateChangedAt: sub.StateChangedAt,
		SourceID:       sub.SourceID,
		Rules:          rules,
		CreatedAt:      sub.CreatedAt,
		UpdatedAt:      sub.UpdatedAt,
	}
}

// subscriberState は監査ログに残す加入者の内容。
type subscriberState struct {
	State    string            `json:"state"`
	SourceID string            `json:"sourceId"`
	Rules    []subscriber.Rule `json:"rules"`
}

func stateOf(sub subscriber.Subscriber) subscriberState {
	return subscriberState{State: string(sub.State), SourceID: sub.SourceID, Rules: sub.Rules}
}

// maxReasonLen は状態の変更の理由の文字数の上限。
const maxReasonLen = 500

// parseReason は理由を読む。required なら空を断る。
func parseReason(s *string, required bool, v *validation) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		if required {
			v.missing = append(v.missing, invalidParam{"reason", "is required"})
		}
		return ""
	}
	if utf8.RuneCountInString(*s) > maxReasonLen {
		v.optional = append(v.optional, invalidParam{"reason", "must be at most 500 characters"})
		return ""
	}
	return *s
}

// parseRules はルールの配列を確かめて返す。誤りは add で記録する（mandatory なら必須項目の不正）。
func parseRules(in []ruleJSON, add func(invalidParam)) []subscriber.Rule {
	if len(in) == 0 || len(in) > maxRules {
		add(invalidParam{"rules", fmt.Sprintf("must have 1 to %d rules", maxRules)})
		return nil
	}
	out := make([]subscriber.Rule, 0, len(in))
	for i, r := range in {
		p := func(name string) string { return fmt.Sprintf("rules[%d].%s", i, name) }
		var rule subscriber.Rule
		ok := true
		bad := func(name, reason string) {
			add(invalidParam{p(name), reason})
			ok = false
		}
		switch {
		case r.TenantID == nil:
			bad("tenantId", "is required")
		case *r.TenantID < 1:
			bad("tenantId", "must be 1 or greater")
		default:
			rule.TenantID = *r.TenantID
		}
		switch {
		case r.NasID == nil:
			bad("nasId", "is required")
		case *r.NasID == "" || len(*r.NasID) > maxNasIDLen:
			bad("nasId", fmt.Sprintf(`must be "*" or 1 to %d bytes`, maxNasIDLen))
		default:
			rule.NasID = *r.NasID
		}
		switch {
		case r.SSIDs == nil:
			bad("ssids", "is required")
		case !validSSIDs(*r.SSIDs):
			bad("ssids", fmt.Sprintf(`must be ["*"] or 1 to %d SSIDs of 1 to %d bytes`, maxSSIDs, maxSSIDLen))
		default:
			rule.SSIDs = *r.SSIDs
		}
		if r.VlanID != nil {
			if *r.VlanID < 1 || *r.VlanID > maxVlanID {
				bad("vlanId", fmt.Sprintf("must be 1 to %d", maxVlanID))
			} else {
				rule.VlanID = *r.VlanID
			}
		}
		if r.SessionTimeout != nil {
			if *r.SessionTimeout < 1 || *r.SessionTimeout > maxSessionTimeout {
				bad("sessionTimeout", fmt.Sprintf("must be 1 to %d seconds", maxSessionTimeout))
			} else {
				rule.SessionTimeout = *r.SessionTimeout
			}
		}
		if ok {
			out = append(out, rule)
		}
	}
	return out
}

// validSSIDs は SSID の配列が ["*"] か、"*" を含まない 1〜32 個の SSID かを返す。
func validSSIDs(ssids []string) bool {
	if len(ssids) == 0 || len(ssids) > maxSSIDs {
		return false
	}
	if slices.Contains(ssids, subscriber.Wildcard) {
		return len(ssids) == 1
	}
	for _, s := range ssids {
		if s == "" || len(s) > maxSSIDLen {
			return false
		}
	}
	return true
}

// knownSource は取得先の登録簿にある ID かを返す。
func (h *Handler) knownSource(id string) bool {
	infos, _ := h.Sources.Infos()
	return slices.ContainsFunc(infos, func(in source.Info) bool { return in.ID == id })
}

func unknownSource(id string) *problem {
	return badParams(causeUnknownSource, invalidParam{"sourceId", fmt.Sprintf("source %q is not in the sources registry", id)})
}

func notFoundSubscriber() *problem {
	return newProblem(http.StatusNotFound, causeUserNotFound, "subscriber not found")
}

func (h *Handler) listSubscribers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix, cursor := q.Get("prefix"), q.Get("cursor")
	if prefix != "" && !imsiPrefixPattern.MatchString(prefix) {
		badParams(causeInvalidQueryParam, invalidParam{"prefix", "must be 1 to 15 digits"}).write(w)
		return
	}
	if cursor != "" && !imsiPattern.MatchString(cursor) {
		badParams(causeInvalidQueryParam, invalidParam{"cursor", "must be a nextCursor value from a previous response"}).write(w)
		return
	}
	limit, p := queryInt(r, "limit", 50, 1, 500)
	if p != nil {
		p.write(w)
		return
	}
	page, err := h.Subs.List(r.Context(), prefix, cursor, int(limit))
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out := subscriberListJSON{Items: make([]subscriberJSON, len(page.Items)), Total: page.Total, NextCursor: page.NextCursor}
	for i, sub := range page.Items {
		out.Items[i] = toSubscriberJSON(sub)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) createSubscriber(w http.ResponseWriter, r *http.Request) {
	var body subscriberCreateJSON
	if p := decodeBody(w, r, &body); p != nil {
		p.write(w)
		return
	}

	var v validation
	var sub subscriber.Subscriber
	switch {
	case body.IMSI == nil:
		v.missing = append(v.missing, invalidParam{"imsi", "is required"})
	case !imsiPattern.MatchString(*body.IMSI):
		v.mandatory = append(v.mandatory, invalidParam{"imsi", "must be 15 digits"})
	default:
		sub.IMSI = *body.IMSI
	}
	// 登録のときの状態は active か inactive（aka-vecgen の設計概要 §7.1 と同じ）。
	switch {
	case body.State == nil:
		v.missing = append(v.missing, invalidParam{"state", "is required"})
	case *body.State != string(subscriber.StateActive) && *body.State != string(subscriber.StateInactive):
		v.mandatory = append(v.mandatory, invalidParam{"state", "must be active or inactive"})
	default:
		sub.State = subscriber.State(*body.State)
	}
	switch {
	case body.SourceID == nil:
		v.missing = append(v.missing, invalidParam{"sourceId", "is required"})
	case !sourceIDPattern.MatchString(*body.SourceID):
		v.mandatory = append(v.mandatory, invalidParam{"sourceId", "must be 1-32 characters of a-z, 0-9 and -"})
	default:
		sub.SourceID = *body.SourceID
	}
	if body.Rules == nil {
		v.missing = append(v.missing, invalidParam{"rules", "is required"})
	} else {
		sub.Rules = parseRules(*body.Rules, func(p invalidParam) { v.mandatory = append(v.mandatory, p) })
	}
	if p := v.problem(); p != nil {
		p.write(w)
		return
	}
	if !h.knownSource(sub.SourceID) {
		unknownSource(sub.SourceID).write(w)
		return
	}

	err := h.Subs.Create(r.Context(), sub)
	switch {
	case errors.Is(err, subscriber.ErrExists):
		newProblem(http.StatusConflict, causeSubscriberExists, "subscriber already exists").write(w)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	h.audit(r, "subscriber.create", sub.IMSI, stateOf(sub))

	created, err := h.Subs.Get(r.Context(), sub.IMSI)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("Location", "/admin/v2/subscribers/"+sub.IMSI)
	writeJSON(w, http.StatusCreated, toSubscriberJSON(created))
}

// loadSubscriber はパスの IMSI の加入者を読む。見つからなければ 404 を書き込んで false を返す。
func (h *Handler) loadSubscriber(w http.ResponseWriter, r *http.Request) (subscriber.Subscriber, bool) {
	imsi := r.PathValue("imsi")
	if !imsiPattern.MatchString(imsi) {
		notFoundSubscriber().write(w)
		return subscriber.Subscriber{}, false
	}
	sub, err := h.Subs.Get(r.Context(), imsi)
	if errors.Is(err, subscriber.ErrNotFound) {
		notFoundSubscriber().write(w)
		return subscriber.Subscriber{}, false
	}
	if err != nil {
		h.internalError(w, r, err)
		return subscriber.Subscriber{}, false
	}
	return sub, true
}

func (h *Handler) getSubscriber(w http.ResponseWriter, r *http.Request) {
	if sub, ok := h.loadSubscriber(w, r); ok {
		writeJSON(w, http.StatusOK, toSubscriberJSON(sub))
	}
}

// change は監査ログに残す変更前後の値。
type change struct {
	From any `json:"from"`
	To   any `json:"to"`
}

func (h *Handler) updateSubscriber(w http.ResponseWriter, r *http.Request) {
	fields, p := decodePatch(w, r)
	if p != nil {
		p.write(w)
		return
	}

	var v validation
	var patch subscriber.Patch
	// 状態は専用の口で変える。
	if _, ok := fields["state"]; ok {
		delete(fields, "state")
		v.optional = append(v.optional, invalidParam{"state", "use PUT /subscribers/{imsi}/state or POST /subscribers/{imsi}/reinstate"})
	}
	if s := patchField[string](fields, "sourceId", &v); s != nil {
		if sourceIDPattern.MatchString(*s) {
			patch.SourceID = s
		} else {
			v.optional = append(v.optional, invalidParam{"sourceId", "must be 1-32 characters of a-z, 0-9 and -"})
		}
	}
	if rules := patchField[[]ruleJSON](fields, "rules", &v); rules != nil {
		parsed := parseRules(*rules, func(p invalidParam) { v.optional = append(v.optional, p) })
		if len(parsed) == len(*rules) {
			patch.Rules = &parsed
		}
	}
	rejectUnknown(fields, &v)
	if p := v.problem(); p != nil {
		p.write(w)
		return
	}
	if patch.SourceID != nil && !h.knownSource(*patch.SourceID) {
		unknownSource(*patch.SourceID).write(w)
		return
	}
	before, found := h.loadSubscriber(w, r)
	if !found {
		return
	}
	err := h.Subs.Update(r.Context(), before.IMSI, patch)
	switch {
	case errors.Is(err, subscriber.ErrNotFound):
		notFoundSubscriber().write(w)
		return
	case errors.Is(err, subscriber.ErrTerminated):
		newProblem(http.StatusConflict, causeSubscriberTerminated, "a terminated subscriber cannot be changed").write(w)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	// 監査ログには、指定された項目の前後の値を残す。
	detail := map[string]any{}
	if patch.SourceID != nil {
		detail["sourceId"] = change{before.SourceID, *patch.SourceID}
	}
	if patch.Rules != nil {
		detail["rules"] = change{before.Rules, *patch.Rules}
	}
	h.audit(r, "subscriber.update", before.IMSI, detail)
	if after, ok := h.loadSubscriber(w, r); ok {
		writeJSON(w, http.StatusOK, toSubscriberJSON(after))
	}
}

func (h *Handler) deleteSubscriber(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.loadSubscriber(w, r)
	if !ok {
		return
	}
	err := h.Subs.Delete(r.Context(), sub.IMSI)
	switch {
	case errors.Is(err, subscriber.ErrNotFound):
		notFoundSubscriber().write(w)
		return
	case errors.Is(err, subscriber.ErrActive):
		newProblem(http.StatusConflict, causeSubscriberActive, "an active subscriber cannot be deleted; set it inactive or terminated first").write(w)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	// セッションは消さない（期限で消える。MVP の設計概要 §11.2）。削除の時点の内容を残す。
	h.audit(r, "subscriber.delete", sub.IMSI, stateOf(sub))
	w.WriteHeader(http.StatusNoContent)
}

// setSubscriberState は状態を変える（active ⇔ inactive、terminated へ）。terminated からは reinstate だけで戻す。
// 同じ状態への変更は何もせず 200 を返し、監査ログには残す（aka-vecgen の設計概要 §7.1 と同じ）。
func (h *Handler) setSubscriberState(w http.ResponseWriter, r *http.Request) {
	var body stateChangeJSON
	if p := decodeBody(w, r, &body); p != nil {
		p.write(w)
		return
	}
	var v validation
	var to subscriber.State
	switch {
	case body.State == nil:
		v.missing = append(v.missing, invalidParam{"state", "is required"})
	case !subscriber.State(*body.State).Valid():
		v.mandatory = append(v.mandatory, invalidParam{"state", "must be active, inactive or terminated"})
	default:
		to = subscriber.State(*body.State)
	}
	reason := parseReason(body.Reason, false, &v)
	if p := v.problem(); p != nil {
		p.write(w)
		return
	}
	imsi := r.PathValue("imsi")
	if !imsiPattern.MatchString(imsi) {
		notFoundSubscriber().write(w)
		return
	}
	from, unchanged, err := h.Subs.SetState(r.Context(), imsi, to)
	switch {
	case errors.Is(err, subscriber.ErrNotFound):
		notFoundSubscriber().write(w)
		return
	case errors.Is(err, subscriber.ErrTransitionNotAllowed):
		newProblem(http.StatusConflict, causeTransitionNotAllowed,
			"a terminated subscriber can only be returned to inactive with POST /subscribers/{imsi}/reinstate").write(w)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	detail := map[string]any{"state": change{from, to}}
	if unchanged {
		detail["unchanged"] = true
	}
	if reason != "" {
		detail["reason"] = reason
	}
	h.audit(r, "subscriber.state", imsi, detail)
	if after, ok := h.loadSubscriber(w, r); ok {
		writeJSON(w, http.StatusOK, toSubscriberJSON(after))
	}
}

// reinstateSubscriber は terminated の加入者を inactive に戻す。理由は必須。
// システム管理者だけに許すことは aka-ossbss が守る。
func (h *Handler) reinstateSubscriber(w http.ResponseWriter, r *http.Request) {
	var body reinstateJSON
	if p := decodeBody(w, r, &body); p != nil {
		p.write(w)
		return
	}
	var v validation
	reason := parseReason(body.Reason, true, &v)
	if p := v.problem(); p != nil {
		p.write(w)
		return
	}
	imsi := r.PathValue("imsi")
	if !imsiPattern.MatchString(imsi) {
		notFoundSubscriber().write(w)
		return
	}
	from, err := h.Subs.Reinstate(r.Context(), imsi)
	switch {
	case errors.Is(err, subscriber.ErrNotFound):
		notFoundSubscriber().write(w)
		return
	case errors.Is(err, subscriber.ErrTransitionNotAllowed):
		newProblem(http.StatusConflict, causeTransitionNotAllowed, "only a terminated subscriber can be reinstated").write(w)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	h.audit(r, "subscriber.reinstate", imsi, map[string]any{"state": change{from, subscriber.StateInactive}, "reason": reason})
	if after, ok := h.loadSubscriber(w, r); ok {
		writeJSON(w, http.StatusOK, toSubscriberJSON(after))
	}
}

// maxBatchGet は一括取得で受け付ける IMSI の数の上限。
const maxBatchGet = 500

type batchGetJSON struct {
	IMSIs *[]string `json:"imsis"`
}

type batchGetResultJSON struct {
	Items    []subscriberJSON `json:"items"`
	NotFound []string         `json:"notFound"`
}

// batchGetSubscribers は IMSI を 500 件まで受け取り、ある加入者と、ない IMSI を返す。
// 台帳との突き合わせと一括投入の前の確認に使う。重なった IMSI は 1 つにまとめ、応答は要求の順にする。
func (h *Handler) batchGetSubscribers(w http.ResponseWriter, r *http.Request) {
	var body batchGetJSON
	if p := decodeBody(w, r, &body); p != nil {
		p.write(w)
		return
	}
	switch {
	case body.IMSIs == nil:
		badParams(causeMandatoryIEMissing, invalidParam{"imsis", "is required"}).write(w)
		return
	case len(*body.IMSIs) == 0 || len(*body.IMSIs) > maxBatchGet:
		badParams(causeMandatoryIEIncorrect, invalidParam{"imsis", fmt.Sprintf("must have 1 to %d items", maxBatchGet)}).write(w)
		return
	}
	var imsis []string
	seen := map[string]bool{}
	for i, imsi := range *body.IMSIs {
		if !imsiPattern.MatchString(imsi) {
			badParams(causeMandatoryIEIncorrect, invalidParam{fmt.Sprintf("imsis[%d]", i), "must be 15 digits"}).write(w)
			return
		}
		if !seen[imsi] {
			seen[imsi] = true
			imsis = append(imsis, imsi)
		}
	}
	subs, err := h.Subs.GetMany(r.Context(), imsis)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	found := map[string]subscriber.Subscriber{}
	for _, sub := range subs {
		found[sub.IMSI] = sub
	}
	out := batchGetResultJSON{Items: []subscriberJSON{}, NotFound: []string{}}
	for _, imsi := range imsis {
		if sub, ok := found[imsi]; ok {
			out.Items = append(out.Items, toSubscriberJSON(sub))
		} else {
			out.NotFound = append(out.NotFound, imsi)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
