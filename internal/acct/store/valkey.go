// Package store はValkeyへのデータアクセスを提供する。
package store

import (
	"fmt"

	"github.com/oyaguma3/aka-radius/internal/acct/config"
	"github.com/oyaguma3/aka-radius/internal/valkey"
	"github.com/redis/go-redis/v9"
)

// ValkeyClient はValkeyクライアントをラップする。
type ValkeyClient struct {
	client *redis.Client
}

// NewValkeyClient は新しいValkeyClientを生成する（接続を確かめる）。
func NewValkeyClient(cfg *config.Config) (*ValkeyClient, error) {
	client, err := valkey.NewClient(&valkey.Options{
		Addr:            cfg.ValkeyAddr(),
		Password:        cfg.ValkeyPassword,
		ConnectTimeout:  config.ValkeyConnectTimeout,
		ReadTimeout:     config.ValkeyCommandTimeout,
		WriteTimeout:    config.ValkeyCommandTimeout,
		PoolSize:        config.ValkeyPoolSize,
		MinIdleConns:    2,
		MaxRetries:      config.ValkeyMaxRetries,
		MinRetryBackoff: config.ValkeyMinRetryDelay,
		MaxRetryBackoff: config.ValkeyMaxRetryDelay,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Valkey: %w", err)
	}
	return &ValkeyClient{client: client}, nil
}

// Close は接続を閉じる。
func (v *ValkeyClient) Close() error {
	return v.client.Close()
}

// Client は内部のredis.Clientを返す。
func (v *ValkeyClient) Client() *redis.Client {
	return v.client
}
