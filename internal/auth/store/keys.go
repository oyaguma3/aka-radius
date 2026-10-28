package store

// Valkeyキープレフィックス（D-02準拠）
const (
	KeyPrefixClient     = "client:"   // RADIUSクライアント設定
	KeyPrefixEAPContext = "eap:"      // EAP認証コンテキスト
	KeyPrefixSession    = "sess:"     // アクティブセッション
	KeyPrefixUserIndex  = "idx:user:" // ユーザー検索インデックス
)
