package validate

import (
	"strings"
	"testing"
)

func TestDNS1123Name(t *testing.T) {
	// Rejected: traversal, separators, uppercase, bad edges, spaces, over-length.
	bad := []string{"", "../x", "a/b", "..", "Team-A", "-x", "x-", "a b", strings.Repeat("a", 64)}
	for _, s := range bad {
		if DNS1123Name(s) {
			t.Errorf("DNS1123Name(%q) = true, want false", s)
		}
	}
	if DNS1123Name("x..y") {
		t.Error(`DNS1123Name("x..y") = true, want false`)
	}
	// Accepted: DNS-1123 labels, including the 63-char maximum.
	for _, s := range []string{"team-a", "a", "x1", "abc-123", strings.Repeat("a", 63)} {
		if !DNS1123Name(s) {
			t.Errorf("DNS1123Name(%q) = false, want true", s)
		}
	}
}

func TestRequireDNS1123(t *testing.T) {
	if err := RequireDNS1123("name", "ok-name"); err != nil {
		t.Errorf("RequireDNS1123 valid: %v", err)
	}
	err := RequireDNS1123("VM name", "../evil")
	if err == nil || !strings.Contains(err.Error(), "VM name") {
		t.Errorf("RequireDNS1123 invalid: %v", err)
	}
}
