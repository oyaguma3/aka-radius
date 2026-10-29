package source

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// StatusKey は、auth が取得先ごとの遮断器の状態を書くキー（String。JSON）。admin が /status と /sources に出す
// （遮断器は auth のプロセスの中にあるため。MVP の設計概要 §6.1・§11.2。段階 2-5 の K4）。
const StatusKey = "stat:sources"

// BreakerStatus は 1 つの取得先の遮断器の状態。
type BreakerStatus struct {
	// State は closed / half-open / open。
	State string `json:"state"`
	// Since は今の状態になった時刻（auth が起動したか、登録簿を読み直したときは、その時刻）。
	Since time.Time `json:"since"`
}

// Status は auth が書く遮断器の状態の報告。
type Status struct {
	// ReportedAt は auth が報告を書いた時刻（状態が変わったときと 30 秒ごと）。古ければ auth が止まっている。
	ReportedAt time.Time                `json:"reportedAt"`
	Sources    map[string]BreakerStatus `json:"sources"`
}

// StatusStore は遮断器の状態の報告を Valkey に読み書きする。
type StatusStore struct {
	rdb *redis.Client
}

// NewStatusStore は StatusStore を作る。
func NewStatusStore(rdb *redis.Client) *StatusStore {
	return &StatusStore{rdb: rdb}
}

// Write は報告を書く（前の報告を丸ごと置き換える。登録簿から消えた取得先は報告からも消える）。
func (s *StatusStore) Write(ctx context.Context, st Status) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, StatusKey, b, 0).Err()
}

// Read は報告を読む。まだなければ ok は false。
func (s *StatusStore) Read(ctx context.Context) (st Status, ok bool, err error) {
	b, err := s.rdb.Get(ctx, StatusKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return Status{}, false, err
	}
	return st, true, nil
}
