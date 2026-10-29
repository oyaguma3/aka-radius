package session

import (
	"context"
	"fmt"

	"github.com/oyaguma3/aka-radius/internal/auth/config"
	"github.com/oyaguma3/aka-radius/internal/auth/store"
)

// EAPContext はEAP認証コンテキストを表す（D-09 §6.6）。
type EAPContext struct {
	IMSI                 string `redis:"imsi"`
	Stage                string `redis:"stage"`
	EAPType              uint8  `redis:"eap_type"`
	RAND                 string `redis:"rand"`
	AUTN                 string `redis:"autn"`
	XRES                 string `redis:"xres"`
	Kaut                 string `redis:"k_aut"`
	MSK                  string `redis:"msk"`
	SourceID             string `redis:"source_id"` // ベクターを取った取得先（再同期で同じ取得先に要求する）
	ResyncCount          int    `redis:"resync_count"`
	PermanentIDRequested bool   `redis:"permanent_id_requested"`
	// Identity は鍵の導出に使う端末の Identity（最後の AT_IDENTITY か EAP-Response/Identity。RFC 4187 §7）
	Identity string `redis:"identity"`
	// TenantID・RuleIndex は、ベクターを取る前の判定で使った刻印のテナントと、採ったルールの番号（MVP の設計概要 §6.1）
	TenantID  int64 `redis:"tenant_id"`
	RuleIndex int   `redis:"rule_index"`
	// VlanID・SessionTimeout は採ったルールの値（Access-Accept に付ける。0 なら付けない）
	VlanID         int `redis:"vlan_id"`
	SessionTimeout int `redis:"session_timeout"`
}

// contextStore はContextStoreの実装。
type contextStore struct {
	vc *store.ValkeyClient
}

// NewContextStore はContextStoreの新しいインスタンスを生成する。
func NewContextStore(vc *store.ValkeyClient) ContextStore {
	return &contextStore{vc: vc}
}

// Create はEAPコンテキストをValkeyに保存する。
func (s *contextStore) Create(ctx context.Context, traceID string, eapCtx *EAPContext) error {
	key := store.KeyPrefixEAPContext + traceID
	m := store.StructToMap(eapCtx)

	pipe := s.vc.Client().Pipeline()
	pipe.HSet(ctx, key, m)
	pipe.Expire(ctx, key, config.EAPContextTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	return nil
}

// Get はEAPコンテキストをValkeyから取得する。
func (s *contextStore) Get(ctx context.Context, traceID string) (*EAPContext, error) {
	key := store.KeyPrefixEAPContext + traceID
	m, err := s.vc.Client().HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	if len(m) == 0 {
		return nil, ErrContextNotFound
	}

	var eapCtx EAPContext
	if err := store.MapToStruct(m, &eapCtx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextInvalid, err)
	}
	return &eapCtx, nil
}

// Update はEAPコンテキストを部分更新し、TTLをリフレッシュする。
func (s *contextStore) Update(ctx context.Context, traceID string, updates map[string]any) error {
	key := store.KeyPrefixEAPContext + traceID

	exists, err := s.vc.Client().Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	if exists == 0 {
		return ErrContextNotFound
	}

	pipe := s.vc.Client().Pipeline()
	pipe.HSet(ctx, key, updates)
	pipe.Expire(ctx, key, config.EAPContextTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	return nil
}

// Delete はEAPコンテキストを削除する。存在しなくてもエラーにしない。
func (s *contextStore) Delete(ctx context.Context, traceID string) error {
	key := store.KeyPrefixEAPContext + traceID
	if err := s.vc.Client().Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	return nil
}

// Exists はEAPコンテキストの存在を確認する。
func (s *contextStore) Exists(ctx context.Context, traceID string) (bool, error) {
	key := store.KeyPrefixEAPContext + traceID
	n, err := s.vc.Client().Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("%w: %v", store.ErrValkeyUnavailable, err)
	}
	return n > 0, nil
}
