package acct

import (
	"github.com/oyaguma3/aka-radius/internal/acct/session"
)

// Processor はAccounting処理のメインロジック。
type Processor struct {
	sessionManager     session.SessionManager
	duplicateDetector  DuplicateDetector
	identifierResolver session.IdentifierResolver
}

// NewProcessor は新しいProcessorを生成する。
func NewProcessor(
	sm session.SessionManager,
	dd DuplicateDetector,
	ir session.IdentifierResolver,
) *Processor {
	return &Processor{
		sessionManager:     sm,
		duplicateDetector:  dd,
		identifierResolver: ir,
	}
}
