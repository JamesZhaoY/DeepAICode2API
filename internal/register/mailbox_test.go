package register

import (
	"strings"
	"testing"
)

func TestUnsupportedMailBackendsRejected(t *testing.T) {
	for _, name := range []string{"cf", "static", "none"} {
		if _, err := NewProvider(&Config{}, name, "", "a@example.com"); err == nil || !strings.Contains(err.Error(), "仅支持") {
			t.Fatalf("backend %s: %v", name, err)
		}
	}
}
