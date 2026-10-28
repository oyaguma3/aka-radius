package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/auth/eap"
	"github.com/oyaguma3/aka-radius/internal/auth/eap/aka"
	"github.com/oyaguma3/aka-radius/internal/auth/eap/akaprime"
	"github.com/oyaguma3/aka-radius/internal/auth/session"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	"github.com/oyaguma3/aka-radius/internal/logging"
	"github.com/oyaguma3/aka-radius/internal/subscriber"
	eapaka "github.com/oyaguma3/go-eapaka"
)

// SubscriberReader は加入者（状態・取得先・ルール）を読む。
type SubscriberReader interface {
	// Get は加入者を返す。いなければ subscriber.ErrNotFound、形が違えば subscriber.ErrInvalid を返す。
	Get(ctx context.Context, imsi string) (subscriber.Subscriber, error)
}

// StampTrust は、送信元の RADIUS クライアントの刻印を使ってよいか（trust_stamp の印）を引く（MVP の設計概要 §8.3）。
type StampTrust interface {
	TrustsStamp(ctx context.Context, ip string) (bool, error)
}

// EngineImpl はeap.EAPProcessorの実装
type EngineImpl struct {
	vectorClient source.Fetcher
	ctxStore     session.ContextStore
	sessStore    session.SessionStore
	subs         SubscriberReader
	trust        StampTrust
	cfg          *config.Config
}

// NewEngine は新しいEAPエンジンを生成する
func NewEngine(
	vc source.Fetcher,
	cs session.ContextStore,
	ss session.SessionStore,
	subs SubscriberReader,
	trust StampTrust,
	cfg *config.Config,
) *EngineImpl {
	return &EngineImpl{
		vectorClient: vc,
		ctxStore:     cs,
		sessStore:    ss,
		subs:         subs,
		trust:        trust,
		cfg:          cfg,
	}
}

// Process はEAP認証処理のエントリポイント
func (e *EngineImpl) Process(ctx context.Context, req *eap.Request) *eap.Result {
	if len(req.State) == 0 {
		// State無し → 初回Identity処理
		return e.handleIdentity(ctx, req)
	}

	// State有り → TraceID復元、EAPContext取得
	traceID := string(req.State)
	eapCtx, err := e.ctxStore.Get(ctx, traceID)
	if err != nil {
		slog.Warn("EAPコンテキスト取得失敗",
			"event_id", "EAP_CTX_NOT_FOUND",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(0)
	}

	return e.handleSubsequent(ctx, req, traceID, eapCtx)
}

// handleIdentity は初回Identity受信を処理する。
// Identity は RADIUS の User-Name ではなく、EAP-Response/Identity の Type-Data（または AKA-Identity の AT_IDENTITY）から取る
// （MVP の設計概要 §8.1。決定事項 8）。
func (e *EngineImpl) handleIdentity(ctx context.Context, req *eap.Request) *eap.Result {
	var identifier uint8
	var rawIdentity string

	// EAPパケットのType判定（RFC 3748 Identity vs AKA/AKA'）
	if eap.GetEAPType(req.EAPMessage) == eap.EAPTypeIdentity {
		// RFC 3748 EAP-Response/Identity (Type=1)。go-eapakaではパースできないので、バイト列から読む
		identifier = eap.GetEAPIdentifier(req.EAPMessage)
		id, err := eap.IdentityFromResponse(req.EAPMessage)
		if err != nil {
			slog.Warn("Identity解析失敗",
				"event_id", "EAP_IDENTITY_INVALID",
				"trace_id", req.TraceID,
				"error", err,
			)
			return e.buildReject(identifier + 1)
		}
		rawIdentity = id
		slog.Info("EAP-Response/Identity受信",
			"event_id", "EAP_IDENTITY_RECEIVED",
			"trace_id", req.TraceID,
		)
	} else {
		// EAP-AKA/AKA' (Type=23/50) パケットパース
		pkt, err := eap.ParseEAPPacket(req.EAPMessage)
		if err != nil {
			slog.Warn("EAPパケットパース失敗",
				"event_id", "EAP_PARSE_ERR",
				"trace_id", req.TraceID,
				"error", err,
			)
			return e.buildReject(0)
		}

		// Subtype=Identity確認
		if pkt.Subtype != eapaka.SubtypeIdentity {
			slog.Warn("初回リクエストがIdentityではない",
				"event_id", "EAP_NOT_IDENTITY",
				"trace_id", req.TraceID,
				"subtype", pkt.Subtype,
			)
			return &eap.Result{Action: eap.ActionDrop}
		}
		identifier = pkt.Identifier
		id, ok := eap.IdentityFromAKA(pkt)
		if !ok {
			slog.Warn("AKA-IdentityにAT_IDENTITYがない",
				"event_id", "EAP_IDENTITY_INVALID",
				"trace_id", req.TraceID,
			)
			return e.buildReject(identifier + 1)
		}
		rawIdentity = id
	}
	e.logIdentityMismatch(req, rawIdentity)

	// Identity解析
	identity, err := eap.ParseIdentity(rawIdentity)
	if err != nil {
		e.logIdentityError(req.TraceID, rawIdentity, err)
		return e.buildReject(identifier + 1)
	}

	// フル認証誘導（仮名/再認証ID）
	if identity.RequiresFullAuth() {
		return e.handleFullAuthRedirect(ctx, req, identifier, identity)
	}

	// 永続ID処理
	return e.handlePermanentIdentity(ctx, req, identifier, identity, false)
}

// logIdentityMismatch は、EAP の Identity と RADIUS の User-Name が違うときにログに出す（判定には EAP の側を使う）。
func (e *EngineImpl) logIdentityMismatch(req *eap.Request, identity string) {
	if req.UserName == "" || req.UserName == identity {
		return
	}
	slog.Info("EAPのIdentityとUser-Nameが違う（EAPのIdentityを使う）",
		"event_id", "EAP_IDENTITY_MISMATCH",
		"trace_id", req.TraceID,
		"identity", e.maskUserName(identity),
		"user_name", e.maskUserName(req.UserName),
	)
}

// logIdentityError は Identity の解析の失敗をログに出す。
func (e *EngineImpl) logIdentityError(traceID, identity string, err error) {
	if errors.Is(err, eap.ErrUnsupportedIdentity) {
		slog.Warn("非対応のIdentity種別",
			"event_id", "EAP_UNSUPPORTED_TYPE",
			"trace_id", traceID,
			"identity", e.maskUserName(identity),
		)
		return
	}
	slog.Warn("Identity解析失敗",
		"event_id", "EAP_IDENTITY_INVALID",
		"trace_id", traceID,
		"identity", e.maskUserName(identity),
		"error", err,
	)
}

// handleFullAuthRedirect はフル認証への誘導処理を行う
func (e *EngineImpl) handleFullAuthRedirect(ctx context.Context, req *eap.Request, identifier uint8, identity *eap.ParsedIdentity) *eap.Result {
	// EAPContext作成
	eapCtx := &session.EAPContext{
		Stage:                string(eap.StateNew),
		PermanentIDRequested: true,
		EAPType:              identity.EAPType,
	}
	if err := e.ctxStore.Create(ctx, req.TraceID, eapCtx); err != nil {
		slog.Error("EAPコンテキスト作成失敗",
			"event_id", "EAP_CTX_CREATE_ERR",
			"trace_id", req.TraceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	// AKA-Identity Request構築
	identityReq, err := eap.BuildAKAIdentityRequest(identifier+1, identity.EAPType)
	if err != nil {
		slog.Error("Identity Request構築失敗",
			"event_id", "EAP_BUILD_ERR",
			"trace_id", req.TraceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	// Stage更新: WAITING_IDENTITY
	if err := e.ctxStore.Update(ctx, req.TraceID, map[string]any{
		"stage": string(eap.StateWaitingIdentity),
	}); err != nil {
		slog.Error("EAPコンテキスト更新失敗",
			"event_id", "EAP_CTX_UPDATE_ERR",
			"trace_id", req.TraceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	return &eap.Result{
		Action:     eap.ActionChallenge,
		EAPMessage: identityReq,
		State:      []byte(req.TraceID),
	}
}

// handlePermanentIdentity は永続IDを受けたときの処理を行う。
// ベクターを取る前に、加入者の有無・状態・刻印・ルールを確かめる（MVP の設計概要 §8.1。決定事項 4）。
// ctxExists は、EAPコンテキストが既にある（AKA-Identity で永続IDを求めた後）かどうか。
func (e *EngineImpl) handlePermanentIdentity(ctx context.Context, req *eap.Request, identifier uint8, identity *eap.ParsedIdentity, ctxExists bool) *eap.Result {
	maskedIMSI := e.maskIMSI(identity.IMSI)

	authz, ok := e.authorize(ctx, req, req.TraceID, identity.IMSI)
	if !ok {
		if ctxExists {
			_ = e.ctxStore.Delete(ctx, req.TraceID)
		}
		return e.buildReject(identifier + 1)
	}

	// 状態遷移: IDENTITY_RECEIVED → WAITING_VECTOR
	if _, err := eap.ValidateTransition(eap.StateIdentityReceived, eap.EventVectorRequest); err != nil {
		slog.Error("状態遷移失敗",
			"event_id", "EAP_STATE_ERR",
			"trace_id", req.TraceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	fields := map[string]any{
		"imsi":            identity.IMSI,
		"stage":           string(eap.StateIdentityReceived),
		"eap_type":        identity.EAPType,
		"identity":        identity.Raw,
		"source_id":       authz.sourceID,
		"tenant_id":       authz.tenantID,
		"rule_index":      authz.ruleIndex,
		"vlan_id":         authz.rule.VlanID,
		"session_timeout": authz.rule.SessionTimeout,
	}
	var err error
	if ctxExists {
		err = e.ctxStore.Update(ctx, req.TraceID, fields)
	} else {
		err = e.ctxStore.Create(ctx, req.TraceID, &session.EAPContext{
			IMSI:           identity.IMSI,
			Stage:          string(eap.StateIdentityReceived),
			EAPType:        identity.EAPType,
			Identity:       identity.Raw,
			SourceID:       authz.sourceID,
			TenantID:       authz.tenantID,
			RuleIndex:      authz.ruleIndex,
			VlanID:         authz.rule.VlanID,
			SessionTimeout: authz.rule.SessionTimeout,
		})
	}
	if err != nil {
		slog.Error("EAPコンテキスト保存失敗",
			"event_id", "EAP_CTX_CREATE_ERR",
			"trace_id", req.TraceID,
			"imsi", maskedIMSI,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	// Vector取得 + Challenge構築
	return e.requestVectorAndBuildChallenge(ctx, identifier, identity, authz.sourceID, req.TraceID)
}

// authorization は、ベクターを取る前の判定の結果。
type authorization struct {
	sourceID  string
	tenantID  int64
	ruleIndex int
	rule      subscriber.Rule
}

// authorize は、加入者の有無・状態・刻印・認可ポリシーのルールを、この順に確かめる（MVP の設計概要 §8.1 の 3）。
// どれかに当たれば、理由をログに残して false を返す（呼び出し側はベクターを取らずに拒否する）。
func (e *EngineImpl) authorize(ctx context.Context, req *eap.Request, traceID, imsi string) (authorization, bool) {
	maskedIMSI := e.maskIMSI(imsi)
	sub, ok := e.loadActiveSubscriber(ctx, traceID, imsi, req.NASIdentifier)
	if !ok {
		return authorization{}, false
	}

	tenantID, ok := e.readStamp(ctx, req, traceID, maskedIMSI)
	if !ok {
		return authorization{}, false
	}

	ssid := subscriber.SSIDOf(req.CalledStation)
	idx, ok := subscriber.Match(sub.Rules, tenantID, req.NASIdentifier, ssid)
	if !ok {
		slog.Warn("ポリシー拒否（一致するルールがない）",
			"event_id", "AUTH_POLICY_DENIED",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"tenant_id", tenantID,
			"nas_identifier", req.NASIdentifier,
			"ssid", ssid,
		)
		return authorization{}, false
	}
	return authorization{sourceID: sub.SourceID, tenantID: tenantID, ruleIndex: idx, rule: sub.Rules[idx]}, true
}

// loadActiveSubscriber は加入者を読み、active であることを確かめる。そうでなければ理由をログに残して false を返す。
func (e *EngineImpl) loadActiveSubscriber(ctx context.Context, traceID, imsi, nasID string) (subscriber.Subscriber, bool) {
	maskedIMSI := e.maskIMSI(imsi)
	sub, err := e.subs.Get(ctx, imsi)
	switch {
	case errors.Is(err, subscriber.ErrNotFound):
		slog.Warn("加入者が登録されていない",
			"event_id", "AUTH_SUBSCRIBER_NOT_FOUND",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"nas_identifier", nasID,
		)
		return subscriber.Subscriber{}, false
	case errors.Is(err, subscriber.ErrInvalid):
		// 本PoC の形の sub: が残っているなど（移し替えはしない。段階 2-3 の G2）
		slog.Error("加入者のデータの形が違う",
			"event_id", "AUTH_SUBSCRIBER_INVALID",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"error", err,
		)
		return subscriber.Subscriber{}, false
	case err != nil:
		slog.Error("加入者を読めない",
			"event_id", "AUTH_SUBSCRIBER_LOOKUP_ERR",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"error", err,
		)
		return subscriber.Subscriber{}, false
	}
	switch sub.State {
	case subscriber.StateActive:
		return sub, true
	case subscriber.StateInactive:
		slog.Warn("加入者が inactive",
			"event_id", "AUTH_SUBSCRIBER_INACTIVE",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"nas_identifier", nasID,
		)
	default:
		slog.Warn("加入者が terminated",
			"event_id", "AUTH_SUBSCRIBER_TERMINATED",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"nas_identifier", nasID,
		)
	}
	return subscriber.Subscriber{}, false
}

// readStamp はテナントの刻印を読む（MVP の設計概要 §8.3）。
// 送信元の RADIUS クライアントに「刻印を信頼する」の印がなければ、刻印はないものとして扱う。
// ルールはどれもテナントを持つので、刻印がなければ拒否する。2 つ以上ある、10 進の 1 以上の数でない場合も拒否する。
func (e *EngineImpl) readStamp(ctx context.Context, req *eap.Request, traceID, maskedIMSI string) (int64, bool) {
	stamps := req.Stamps
	if len(stamps) > 0 {
		trusted, err := e.trust.TrustsStamp(ctx, req.SrcIP)
		if err != nil {
			slog.Error("RADIUSクライアントの刻印の信頼の印を読めない",
				"event_id", "AUTH_STAMP_LOOKUP_ERR",
				"trace_id", traceID,
				"src_ip", req.SrcIP,
				"error", err,
			)
			return 0, false
		}
		if !trusted {
			slog.Debug("刻印を信頼しない送信元なので刻印を使わない",
				"event_id", "AUTH_STAMP_UNTRUSTED",
				"trace_id", traceID,
				"src_ip", req.SrcIP,
			)
			stamps = nil
		}
	}
	switch len(stamps) {
	case 0:
		slog.Warn("刻印がない",
			"event_id", "AUTH_STAMP_MISSING",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"src_ip", req.SrcIP,
			"nas_identifier", req.NASIdentifier,
		)
		return 0, false
	case 1:
	default:
		slog.Warn("刻印が2つ以上ある",
			"event_id", "AUTH_STAMP_MULTIPLE",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"src_ip", req.SrcIP,
			"count", len(stamps),
		)
		return 0, false
	}
	tenantID, err := strconv.ParseInt(string(stamps[0]), 10, 64)
	if err != nil || tenantID < 1 || !isDecimal(stamps[0]) {
		slog.Warn("刻印の値が10進の数でない",
			"event_id", "AUTH_STAMP_INVALID",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"src_ip", req.SrcIP,
			"stamp", strconv.Quote(string(stamps[0])),
		)
		return 0, false
	}
	return tenantID, true
}

// isDecimal は b が 10 進の数字だけかを返す（strconv.ParseInt が受ける `+` を断る）。
func isDecimal(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(b) > 0
}

// requestVectorAndBuildChallenge はVector取得→鍵導出→Challenge構築を行う
func (e *EngineImpl) requestVectorAndBuildChallenge(
	ctx context.Context,
	identifier uint8,
	identity *eap.ParsedIdentity,
	sourceID string,
	traceID string,
) *eap.Result {
	maskedIMSI := e.maskIMSI(identity.IMSI)

	// 加入者の取得先から 1 つのベクターを取る（MVP の設計概要 §7.2）
	vecResp, err := e.vectorClient.Fetch(ctx, &source.Request{
		IMSI:     identity.IMSI,
		SourceID: sourceID,
		Method:   methodOf(identity.EAPType),
		ANID:     e.cfg.NetworkName,
		TraceID:  traceID,
	})
	if err != nil {
		e.logVectorError(err, traceID, maskedIMSI)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(identifier + 1)
	}

	// 鍵導出（EAP-AKA' では取得先が導出した CK'・IK' から MK 以降を導出する。契約 §6.4）
	kAut, msk := deriveKeys(identity, vecResp)

	// EAPContext更新
	updates := map[string]any{
		"stage":     string(eap.StateChallengeSent),
		"rand":      hex.EncodeToString(vecResp.RAND),
		"autn":      hex.EncodeToString(vecResp.AUTN),
		"xres":      hex.EncodeToString(vecResp.XRES),
		"k_aut":     hex.EncodeToString(kAut),
		"msk":       hex.EncodeToString(msk),
		"source_id": vecResp.SourceID,
	}
	if err := e.ctxStore.Update(ctx, traceID, updates); err != nil {
		slog.Error("EAPコンテキスト更新失敗",
			"event_id", "EAP_CTX_UPDATE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	// Challenge構築
	challengeMsg, err := e.buildChallenge(identifier+1, identity, vecResp, kAut)
	if err != nil {
		slog.Error("Challenge構築失敗",
			"event_id", "EAP_BUILD_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(identifier + 1)
	}

	slog.Info("Challenge送信",
		"event_id", "EAP_CHALLENGE_SENT",
		"trace_id", traceID,
		"imsi", maskedIMSI,
		"eap_type", identity.EAPType,
		"source_id", vecResp.SourceID,
	)

	return &eap.Result{
		Action:     eap.ActionChallenge,
		EAPMessage: challengeMsg,
		State:      []byte(traceID),
		IMSI:       identity.IMSI,
	}
}

// deriveKeys は K_aut と MSK を導出する。Identity は鍵の導出に使う端末の Identity（RFC 4187 §7）。
func deriveKeys(identity *eap.ParsedIdentity, v *source.Vector) (kAut, msk []byte) {
	if identity.IsAKAPrime() {
		keys := akaprime.DeriveKeys(identity.Raw, v.CK, v.IK)
		return keys.K_aut, keys.MSK
	}
	keys := aka.DeriveKeys(identity.Raw, v.CK, v.IK)
	return keys.K_aut, keys.MSK
}

// buildChallenge は AKA-Challenge を作る。
func (e *EngineImpl) buildChallenge(identifier uint8, identity *eap.ParsedIdentity, v *source.Vector, kAut []byte) ([]byte, error) {
	if identity.IsAKAPrime() {
		return akaprime.BuildChallenge(identifier, v.RAND, v.AUTN, e.cfg.NetworkName, kAut)
	}
	return aka.BuildChallenge(identifier, v.RAND, v.AUTN, kAut)
}

// handleSubsequent はState有りの後続リクエストを処理する
func (e *EngineImpl) handleSubsequent(ctx context.Context, req *eap.Request, traceID string, eapCtx *session.EAPContext) *eap.Result {
	// EAPパケットパース
	pkt, err := eap.ParseEAPPacket(req.EAPMessage)
	if err != nil {
		slog.Warn("EAPパケットパース失敗",
			"event_id", "EAP_PARSE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(0)
	}

	// Subtype分岐
	switch pkt.Subtype {
	case eapaka.SubtypeChallenge:
		return e.handleChallengeResponse(ctx, req, traceID, eapCtx, pkt)

	case eapaka.SubtypeSynchronizationFailure:
		return e.handleResync(ctx, traceID, eapCtx, pkt)

	case eapaka.SubtypeAuthenticationReject:
		slog.Warn("Authentication-Reject受信",
			"event_id", "EAP_AUTH_REJECT",
			"trace_id", traceID,
			"imsi", e.maskIMSI(eapCtx.IMSI),
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)

	case eapaka.SubtypeClientError:
		slog.Warn("Client-Error受信",
			"event_id", "EAP_CLIENT_ERROR",
			"trace_id", traceID,
			"imsi", e.maskIMSI(eapCtx.IMSI),
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)

	case eapaka.SubtypeIdentity:
		// WAITING_IDENTITY状態の場合のみ受け入れ
		if eap.EAPState(eapCtx.Stage) == eap.StateWaitingIdentity {
			return e.handleIdentityResponse(ctx, req, traceID, pkt)
		}
		slog.Warn("不正なタイミングでIdentity受信",
			"event_id", "EAP_UNEXPECTED_IDENTITY",
			"trace_id", traceID,
			"stage", eapCtx.Stage,
		)
		return e.buildReject(pkt.Identifier + 1)

	default:
		slog.Warn("未知のSubtype",
			"event_id", "EAP_UNKNOWN_SUBTYPE",
			"trace_id", traceID,
			"subtype", pkt.Subtype,
		)
		return e.buildReject(pkt.Identifier + 1)
	}
}

// handleIdentityResponse はWAITING_IDENTITY状態でIdentity応答（AT_IDENTITY）を処理する
func (e *EngineImpl) handleIdentityResponse(ctx context.Context, req *eap.Request, traceID string, pkt *eapaka.Packet) *eap.Result {
	rawIdentity, ok := eap.IdentityFromAKA(pkt)
	if !ok {
		slog.Warn("AKA-IdentityにAT_IDENTITYがない",
			"event_id", "EAP_IDENTITY_INVALID",
			"trace_id", traceID,
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}
	e.logIdentityMismatch(req, rawIdentity)

	identity, err := eap.ParseIdentity(rawIdentity)
	if err != nil {
		e.logIdentityError(traceID, rawIdentity, err)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	if !identity.IsPermanent() {
		slog.Warn("永続ID応答が仮名/再認証ID",
			"event_id", "EAP_IDENTITY_INVALID",
			"trace_id", traceID,
			"identity", e.maskUserName(rawIdentity),
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	// 状態遷移: WAITING_IDENTITY → IDENTITY_RECEIVED
	if _, err := eap.ValidateTransition(eap.StateWaitingIdentity, eap.EventPermanentIdentity); err != nil {
		slog.Error("状態遷移失敗",
			"event_id", "EAP_STATE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	return e.handlePermanentIdentity(ctx, req, pkt.Identifier, identity, true)
}

// handleChallengeResponse はChallenge応答を検証して認証結果を返す
func (e *EngineImpl) handleChallengeResponse(ctx context.Context, req *eap.Request, traceID string, eapCtx *session.EAPContext, pkt *eapaka.Packet) *eap.Result {
	maskedIMSI := e.maskIMSI(eapCtx.IMSI)

	// 状態遷移検証
	if eap.EAPState(eapCtx.Stage) != eap.StateChallengeSent {
		slog.Warn("不正な状態でChallenge応答受信",
			"event_id", "EAP_STATE_ERR",
			"trace_id", traceID,
			"stage", eapCtx.Stage,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	// Valkey保存値の復元
	kAut, err1 := hex.DecodeString(eapCtx.Kaut)
	xres, err2 := hex.DecodeString(eapCtx.XRES)
	mskBytes, err3 := hex.DecodeString(eapCtx.MSK)
	if err := errors.Join(err1, err2, err3); err != nil {
		slog.Error("EAPコンテキストの値の復元失敗",
			"event_id", "EAP_CTX_DECODE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	// Challenge応答検証（AKA/AKA'分岐）
	var verifyErr error
	if eapCtx.EAPType == eapaka.TypeAKAPrime {
		verifyErr = akaprime.VerifyChallengeResponse(pkt, kAut, xres)
	} else {
		verifyErr = aka.VerifyChallengeResponse(pkt, kAut, xres)
	}

	if verifyErr != nil {
		var eventID string
		if errors.Is(verifyErr, eap.ErrMACInvalid) {
			eventID = "AUTH_MAC_INVALID"
		} else if errors.Is(verifyErr, eap.ErrRESMismatch) {
			eventID = "AUTH_RES_MISMATCH"
		} else {
			eventID = "AUTH_VERIFY_FAIL"
		}
		slog.Warn("Challenge応答検証失敗",
			"event_id", eventID,
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"error", verifyErr,
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	// Accept の前に状態だけもう一度確かめる（判定から数秒の間に止めた場合のため。MVP の設計概要 §8.1 の 6）
	if _, ok := e.loadActiveSubscriber(ctx, traceID, eapCtx.IMSI, req.NASIdentifier); !ok {
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	// セッション作成
	sessionID := session.GenerateSessionID()
	sess := &session.Session{
		IMSI:          eapCtx.IMSI,
		TenantID:      eapCtx.TenantID,
		NasIP:         req.SrcIP,
		NasIdentifier: req.NASIdentifier,
		StartTime:     time.Now().Unix(),
	}
	if err := e.sessStore.Create(ctx, sessionID, sess); err != nil {
		slog.Error("セッション作成失敗",
			"event_id", "SESSION_CREATE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(pkt.Identifier + 1)
	}
	if err := e.sessStore.AddUserIndex(ctx, eapCtx.IMSI, sessionID); err != nil {
		slog.Warn("ユーザーインデックス追加失敗",
			"event_id", "SESSION_INDEX_ERR",
			"trace_id", traceID,
			"error", err,
		)
		// インデックス失敗は致命的ではない
	}

	// EAPContext削除
	_ = e.ctxStore.Delete(ctx, traceID)

	// EAP-Success構築
	eapSuccess, _ := eap.BuildEAPSuccess(pkt.Identifier + 1)

	var vlanID string
	if eapCtx.VlanID > 0 {
		vlanID = strconv.Itoa(eapCtx.VlanID)
	}

	slog.Info("認証成功",
		"event_id", "AUTH_SUCCESS",
		"trace_id", traceID,
		"imsi", maskedIMSI,
		"tenant_id", eapCtx.TenantID,
		"rule_index", eapCtx.RuleIndex,
		"nas_identifier", req.NASIdentifier,
		"session_id", sessionID,
	)

	return &eap.Result{
		Action:         eap.ActionAccept,
		EAPMessage:     eapSuccess,
		IMSI:           eapCtx.IMSI,
		SessionID:      sessionID,
		MSK:            mskBytes,
		VlanID:         vlanID,
		SessionTimeout: eapCtx.SessionTimeout,
	}
}

// handleResync は再同期失敗応答を処理する
func (e *EngineImpl) handleResync(ctx context.Context, traceID string, eapCtx *session.EAPContext, pkt *eapaka.Packet) *eap.Result {
	maskedIMSI := e.maskIMSI(eapCtx.IMSI)

	// 状態遷移検証
	if eap.EAPState(eapCtx.Stage) != eap.StateChallengeSent {
		slog.Warn("不正な状態で再同期応答受信",
			"event_id", "EAP_STATE_ERR",
			"trace_id", traceID,
			"stage", eapCtx.Stage,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	// 再同期回数チェック
	if eapCtx.ResyncCount >= config.MaxResyncCount {
		slog.Warn("再同期上限超過",
			"event_id", "AUTH_RESYNC_LIMIT",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"resync_count", eapCtx.ResyncCount,
		)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	// AT_AUTS抽出
	atAuts, found := eap.GetAttribute[*eapaka.AtAuts](pkt)
	if !found {
		slog.Warn("AT_AUTSが見つからない",
			"event_id", "EAP_PARSE_ERR",
			"trace_id", traceID,
			"imsi", maskedIMSI,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	// 直前の Challenge の RAND と AUTS で、同じ取得先から取り直す（契約 §8）
	prevRAND, err := hex.DecodeString(eapCtx.RAND)
	if err != nil || len(prevRAND) != 16 {
		slog.Error("EAPコンテキストのRANDが不正",
			"event_id", "EAP_CTX_INVALID",
			"trace_id", traceID,
			"imsi", maskedIMSI,
		)
		return e.buildReject(pkt.Identifier + 1)
	}
	vecResp, err := e.vectorClient.Fetch(ctx, &source.Request{
		IMSI:     eapCtx.IMSI,
		SourceID: eapCtx.SourceID,
		Method:   methodOf(eapCtx.EAPType),
		ANID:     e.cfg.NetworkName,
		Resync:   &source.Resync{RAND: prevRAND, AUTS: atAuts.Auts},
		TraceID:  traceID,
	})
	if err != nil {
		e.logVectorError(err, traceID, maskedIMSI)
		_ = e.ctxStore.Delete(ctx, traceID)
		return e.buildReject(pkt.Identifier + 1)
	}

	// Identity情報復元（鍵導出には、判定のときに保存した端末の Identity を使う）
	identity := &eap.ParsedIdentity{
		Raw:     eapCtx.Identity,
		IMSI:    eapCtx.IMSI,
		EAPType: eapCtx.EAPType,
		Type:    eap.IdentityTypePermanentAKA,
	}
	if eapCtx.EAPType == eapaka.TypeAKAPrime {
		identity.Type = eap.IdentityTypePermanentAKAPrime
	}

	// 新しい鍵導出
	kAut, msk := deriveKeys(identity, vecResp)

	// EAPContext更新（新RAND/AUTN/XRES/Kaut/MSK + resync_count++）
	updates := map[string]any{
		"stage":        string(eap.StateChallengeSent),
		"rand":         hex.EncodeToString(vecResp.RAND),
		"autn":         hex.EncodeToString(vecResp.AUTN),
		"xres":         hex.EncodeToString(vecResp.XRES),
		"k_aut":        hex.EncodeToString(kAut),
		"msk":          hex.EncodeToString(msk),
		"source_id":    vecResp.SourceID,
		"resync_count": eapCtx.ResyncCount + 1,
	}
	if err := e.ctxStore.Update(ctx, traceID, updates); err != nil {
		slog.Error("EAPコンテキスト更新失敗（再同期）",
			"event_id", "EAP_CTX_UPDATE_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	// 新Challenge構築
	challengeMsg, err := e.buildChallenge(pkt.Identifier+1, identity, vecResp, kAut)
	if err != nil {
		slog.Error("Challenge構築失敗（再同期）",
			"event_id", "EAP_BUILD_ERR",
			"trace_id", traceID,
			"error", err,
		)
		return e.buildReject(pkt.Identifier + 1)
	}

	slog.Info("再同期Challenge送信",
		"event_id", "EAP_RESYNC_CHALLENGE",
		"trace_id", traceID,
		"imsi", maskedIMSI,
		"resync_count", eapCtx.ResyncCount+1,
		"source_id", vecResp.SourceID,
	)

	return &eap.Result{
		Action:     eap.ActionChallenge,
		EAPMessage: challengeMsg,
		State:      []byte(traceID),
		IMSI:       eapCtx.IMSI,
	}
}

// methodOf は EAP の Type から取得先に要求する認証の方式を返す。
func methodOf(eapType uint8) source.Method {
	if eapType == eapaka.TypeAKAPrime {
		return source.MethodAKAPrime
	}
	return source.MethodAKA
}

// logVectorError は認証ベクターの取得の誤りをログに記録する（契約 §9.3、MVP の設計概要 §13）。
// どの誤りでも、その認証は EAP-Failure の Access-Reject で拒否する。
func (e *EngineImpl) logVectorError(err error, traceID, maskedIMSI string) {
	se, ok := errors.AsType[*source.Error](err)
	if !ok {
		slog.Error("認証ベクターの取得の不明な誤り",
			"event_id", "VECTOR_UNKNOWN_ERR",
			"trace_id", traceID,
			"imsi", maskedIMSI,
			"error", err,
		)
		return
	}
	attrs := []any{
		"trace_id", traceID,
		"imsi", maskedIMSI,
		"source_id", se.SourceID,
	}
	if se.Status != 0 {
		attrs = append(attrs, "http_status", se.Status)
	}
	if se.Cause != "" {
		attrs = append(attrs, "cause", se.Cause)
	}
	if se.Reason != "" {
		attrs = append(attrs, "reason", se.Reason)
	}
	if se.LatencyMs != 0 {
		attrs = append(attrs, "latency_ms", se.LatencyMs)
	}
	if se.Err != nil {
		attrs = append(attrs, "error", se.Err.Error())
	}
	switch se.Kind {
	case source.KindRejected:
		// 403 は加入者による一般的な拒否。400・501 は設定の食い違い（許していない anId など）なので目立たせる
		if se.Status == 403 {
			slog.Warn("取得先が拒否した", append([]any{"event_id", "VECTOR_REJECTED"}, attrs...)...)
		} else {
			slog.Error("取得先が拒否した（設定の食い違い）", append([]any{"event_id", "VECTOR_REJECTED"}, attrs...)...)
		}
	case source.KindTemporary:
		slog.Error("取得先の一時的な誤り", append([]any{"event_id", "VECTOR_TEMPORARY_ERR"}, attrs...)...)
	case source.KindContractViolation:
		slog.Error("取得先の応答が契約に合わない", append([]any{"event_id", "VECTOR_CONTRACT_VIOLATION"}, attrs...)...)
	case source.KindCircuitOpen:
		slog.Error("取得先の遮断器が開いている", append([]any{"event_id", "VECTOR_CIRCUIT_OPEN"}, attrs...)...)
	case source.KindSourceUnknown:
		slog.Warn("加入者の取得先が決まらない", append([]any{"event_id", "VECTOR_SOURCE_UNKNOWN"}, attrs...)...)
	case source.KindLookupFailed:
		slog.Error("加入者の取得先を読めない", append([]any{"event_id", "VECTOR_SOURCE_LOOKUP_ERR"}, attrs...)...)
	default:
		slog.Error("認証ベクターの取得の不明な誤り", append([]any{"event_id", "VECTOR_UNKNOWN_ERR"}, attrs...)...)
	}
}

// buildReject はEAP-Failure付きReject結果を生成する
func (e *EngineImpl) buildReject(identifier uint8) *eap.Result {
	eapFailure, _ := eap.BuildEAPFailure(identifier)
	return &eap.Result{
		Action:     eap.ActionReject,
		EAPMessage: eapFailure,
	}
}

// maskIMSI はIMSIマスキングのラッパー
func (e *EngineImpl) maskIMSI(imsi string) string {
	return logging.MaskIMSI(imsi, e.cfg.LogMaskIMSI)
}

// maskUserName はIdentity（User-Name の形）内のIMSIをマスキングする
func (e *EngineImpl) maskUserName(userName string) string {
	return logging.MaskUserName(userName, e.cfg.LogMaskIMSI)
}
