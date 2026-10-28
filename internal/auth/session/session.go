package session

import "github.com/google/uuid"

// GenerateSessionID はUUID形式のセッションIDを生成する（Access-Accept の Class に入れる）。
// セッションそのものの読み書きは internal/sessions（auth・acct・admin で共通。MVP の設計概要 §10.1）。
func GenerateSessionID() string {
	return uuid.New().String()
}
