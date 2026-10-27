package source

import "context"

// SubscriberLookup は加入者の取得先の ID を引く（sub:{imsi} の source_id）。
type SubscriberLookup interface {
	// GetSourceID は加入者の取得先の ID を返す。加入者がいない・取得先がないときは空文字列と nil を返す。
	GetSourceID(ctx context.Context, imsi string) (string, error)
}

// Service は加入者の取得先を引き、その取得先から認証ベクターを取る。Fetcher の実装。
type Service struct {
	lookup   SubscriberLookup
	registry *Registry
}

// NewService は Service を作る。
func NewService(lookup SubscriberLookup, registry *Registry) *Service {
	return &Service{lookup: lookup, registry: registry}
}

// Fetch は取得先から 1 つの認証ベクターを取る。
// req.SourceID が空なら加入者から引く。取得先が決まらなければ、取得先を呼ばずに KindSourceUnknown を返す。
func (s *Service) Fetch(ctx context.Context, req *Request) (*Vector, error) {
	id := req.SourceID
	if id == "" {
		var err error
		if id, err = s.lookup.GetSourceID(ctx, req.IMSI); err != nil {
			return nil, &Error{Kind: KindLookupFailed, Err: err}
		}
		if id == "" {
			return nil, &Error{Kind: KindSourceUnknown, Reason: "subscriber has no source_id"}
		}
	}
	c, ok := s.registry.Get(id)
	if !ok {
		return nil, &Error{Kind: KindSourceUnknown, SourceID: id, Reason: "source not in registry"}
	}
	v, err := c.GenerateAV(ctx, req)
	if err != nil {
		return nil, err
	}
	v.SourceID = id
	return v, nil
}
