package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// subscriberStore は加入者（sub:{imsi}）の読み出し。source.SubscriberLookup の実装。
// 段階 2-2 では取得先の ID（source_id）だけを読む。状態とルールは段階 2-3 で足す。
type subscriberStore struct {
	vc *ValkeyClient
}

// NewSubscriberStore は加入者の読み出しを作る。
func NewSubscriberStore(vc *ValkeyClient) *subscriberStore {
	return &subscriberStore{vc: vc}
}

// GetSourceID は加入者の取得先の ID を返す。加入者がいない・source_id がないときは空文字列と nil を返す。
func (s *subscriberStore) GetSourceID(ctx context.Context, imsi string) (string, error) {
	id, err := s.vc.Client().HGet(ctx, KeyPrefixSubscriber+imsi, "source_id").Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		}
		return "", fmt.Errorf("%w: %v", ErrValkeyUnavailable, err)
	}
	return id, nil
}
