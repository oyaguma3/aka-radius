// Package source は認証ベクターの取得先（aka-vecgen など）のクライアントと、取得先の登録簿を提供する。
// 取得先とは、aka-mvpset の認証ベクターAPI の契約（docs/av-api-contract.md）で話す。
// MVP の設計概要 §7、契約の §16.2（R1〜R7）。
package source

import (
	"context"
	"errors"
	"fmt"
)

// Method は認証の方式。
type Method int

const (
	// MethodAKA は EAP-AKA（hssAuthType は EAP_AKA）。
	MethodAKA Method = iota + 1
	// MethodAKAPrime は EAP-AKA'（hssAuthType は EAP_AKA_PRIME。anId が要る）。
	MethodAKAPrime
)

// hssAuthType は要求の本文の hssAuthType と、応答の avType の値を返す。
func (m Method) hssAuthType() string {
	if m == MethodAKAPrime {
		return "EAP_AKA_PRIME"
	}
	return "EAP_AKA"
}

// pathType はパスの hssAuthType の値を返す。
func (m Method) pathType() string {
	if m == MethodAKAPrime {
		return "eap-aka-prime"
	}
	return "eap-aka"
}

// Request は認証ベクターの要求。
type Request struct {
	// IMSI は加入者の IMSI（数字 5〜15 桁）。
	IMSI string
	// SourceID は取得先の ID。空なら加入者（sub:{imsi} の source_id）から引く。
	// 再同期では、直前の Challenge で使った取得先を入れる。
	SourceID string
	// Method は認証の方式。
	Method Method
	// ANID は EAP-AKA' のネットワーク名（契約 §6.4）。同じ値を AT_KDF_INPUT に使う。
	ANID string
	// Resync は再同期の情報。再同期でなければ nil。
	Resync *Resync
	// TraceID は認証のトレースID（X-Trace-ID で送る）。
	TraceID string
}

// Resync は再同期の情報（契約 §8）。
type Resync struct {
	RAND []byte // 直前の Challenge の RAND（16 バイト）
	AUTS []byte // 端末の AUTS（14 バイト）
}

// Vector は認証ベクター。EAP-AKA' では CK・IK に CK'・IK' が入る（取得先が導出したもの）。
type Vector struct {
	RAND []byte // 16 バイト
	AUTN []byte // 16 バイト
	XRES []byte // 4〜16 バイト
	CK   []byte // 16 バイト（EAP-AKA' では CK'）
	IK   []byte // 16 バイト（EAP-AKA' では IK'）
	// SourceID はベクターを返した取得先の ID。
	SourceID string
}

// Fetcher は認証ベクターを取る（engine が使う）。
type Fetcher interface {
	// Fetch は取得先から 1 つの認証ベクターを取る。誤りは *Error で返す。
	Fetch(ctx context.Context, req *Request) (*Vector, error)
}

// Kind は誤りの分類（契約 §9.3）。
type Kind int

const (
	// KindRejected は確定した拒否（400・403・501）。遮断器には数えない。
	KindRejected Kind = iota + 1
	// KindTemporary は一時的な誤り（500・503、接続・TLS の失敗、タイムアウト）。遮断器に数える。
	KindTemporary
	// KindContractViolation は契約違反（形の合わない 200、本文のない 4xx、契約にないステータス）。遮断器に数える。
	KindContractViolation
	// KindCircuitOpen は遮断器が開いていて要求を送らなかったこと。
	KindCircuitOpen
	// KindSourceUnknown は加入者に取得先がない、または登録簿にない取得先を指していること。取得先は呼ばない。
	KindSourceUnknown
	// KindLookupFailed は加入者の取得先を Valkey から読めなかったこと。取得先は呼ばない。
	KindLookupFailed
)

// String はログに出す分類の名前を返す。
func (k Kind) String() string {
	switch k {
	case KindRejected:
		return "rejected"
	case KindTemporary:
		return "temporary"
	case KindContractViolation:
		return "contract_violation"
	case KindCircuitOpen:
		return "circuit_open"
	case KindSourceUnknown:
		return "source_unknown"
	case KindLookupFailed:
		return "lookup_failed"
	}
	return "unknown"
}

// Error は認証ベクターの取得の誤り。
type Error struct {
	Kind     Kind
	SourceID string
	// Status は取得先の HTTP ステータス（応答がなければ 0）。
	Status int
	// Cause は ProblemDetails の cause（あれば）。
	Cause string
	// Reason は分類の理由（ログ用。例 "timeout"、"avType mismatch"）。値（鍵など）は入れない。
	Reason string
	// LatencyMs は要求にかかった時間（ミリ秒。要求を送らなければ 0）。
	LatencyMs int64
	// Err はもとの誤り（あれば）。
	Err error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("source %q: %s", e.SourceID, e.Kind)
	if e.Status != 0 {
		s += fmt.Sprintf(" (status %d", e.Status)
		if e.Cause != "" {
			s += ", cause " + e.Cause
		}
		s += ")"
	}
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf は err の分類を返す（*Error でなければ 0）。
func KindOf(err error) Kind {
	var se *Error
	if errors.As(err, &se) {
		return se.Kind
	}
	return 0
}
