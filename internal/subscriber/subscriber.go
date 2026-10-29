// Package subscriber は加入者（sub:{imsi}。状態・取得先・認可ポリシーのルール）の Valkey 上のデータと、
// ルールの評価を提供する。auth（認証の判定）と admin（管理API）が共通で使う（MVP の設計概要 §6）。
package subscriber

import (
	"errors"
	"strings"
	"time"
)

// State は加入者の状態（MVP の設計概要 §6.2。規則は aka-vecgen の設計概要 §7 と同じ）。
type State string

const (
	StateActive     State = "active"
	StateInactive   State = "inactive"
	StateTerminated State = "terminated"
)

// Valid は値が 3 つの状態のどれかかを返す。
func (s State) Valid() bool {
	return s == StateActive || s == StateInactive || s == StateTerminated
}

// Rule は認可ポリシーのルール（MVP の設計概要 §8.2）。
type Rule struct {
	// TenantID は刻印のテナントの ID（1 以上。必須）
	TenantID int64 `json:"tenantId"`
	// NasID は NAS-Identifier（`*` か完全一致。大文字と小文字を区別する）
	NasID string `json:"nasId"`
	// SSIDs は SSID の配列（`["*"]` か SSID。大文字と小文字を区別しない）
	SSIDs []string `json:"ssids"`
	// VlanID は Access-Accept の VLAN（1〜4094。0 なら付けない）
	VlanID int `json:"vlanId,omitzero"`
	// SessionTimeout は Access-Accept の Session-Timeout（秒。0 なら付けない）
	SessionTimeout int `json:"sessionTimeout,omitzero"`
}

// Subscriber は加入者。
type Subscriber struct {
	IMSI           string
	State          State
	StateChangedAt time.Time
	// SourceID は認証ベクターの取得先の ID（取得先の登録簿の id）
	SourceID  string
	Rules     []Rule
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Wildcard は、ルールの NAS-Identifier と SSID で「すべて」を表す値。
const Wildcard = "*"

// Match は、テナント・NAS-Identifier・SSID がすべて一致した最初のルールの番号（0 始まり）を返す。
// 一致するルールがなければ false を返す（本PoC の default はなくし、一致しなければ拒否する。MVP の設計概要 §8.2）。
func Match(rules []Rule, tenantID int64, nasID, ssid string) (int, bool) {
	for i, r := range rules {
		if r.TenantID != tenantID {
			continue
		}
		if r.NasID != Wildcard && r.NasID != nasID {
			continue
		}
		if !matchSSID(r.SSIDs, ssid) {
			continue
		}
		return i, true
	}
	return 0, false
}

func matchSSID(ssids []string, ssid string) bool {
	for _, s := range ssids {
		if s == Wildcard || strings.EqualFold(s, ssid) {
			return true
		}
	}
	return false
}

// SSIDOf は Called-Station-Id から SSID を取り出す（最初の `:` より後ろ。`:` がなければ全体。本PoC と同じ）。
func SSIDOf(calledStationID string) string {
	if _, after, found := strings.Cut(calledStationID, ":"); found {
		return after
	}
	return calledStationID
}

var (
	// ErrNotFound は加入者がいないことを表す。
	ErrNotFound = errors.New("subscriber not found")
	// ErrExists は加入者が既にいることを表す。
	ErrExists = errors.New("subscriber already exists")
	// ErrActive は active の加入者を削除しようとしたことを表す。
	ErrActive = errors.New("subscriber is active")
	// ErrTerminated は terminated の加入者を変更しようとしたことを表す。
	ErrTerminated = errors.New("subscriber is terminated")
	// ErrTransitionNotAllowed は許されない状態の変更を表す。
	ErrTransitionNotAllowed = errors.New("state transition not allowed")
	// ErrInvalid は、Valkey の加入者の形が違う（本PoC の形の sub: が残っているなど）ことを表す。
	ErrInvalid = errors.New("subscriber data is invalid")
)
