package session

import (
	"regexp"
	"testing"
)

func TestGenerateSessionID(t *testing.T) {
	id := GenerateSessionID()
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !uuidRegex.MatchString(id) {
		t.Errorf("GenerateSessionID() = %q, not UUID format", id)
	}
}

func TestGenerateSessionIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := GenerateSessionID()
		if seen[id] {
			t.Fatalf("duplicate session ID generated: %s", id)
		}
		seen[id] = true
	}
}
