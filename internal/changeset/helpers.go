package changeset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/validate"
)

// authorEmail derives a stable noreply commit email from a k8s username, which is
// not itself an email (e.g. "system:serviceaccount:tenant-a:viewer-a" or
// "kube:admin"). Colons/slashes/spaces become dots so it's a valid local-part.
func authorEmail(username string) string {
	local := strings.Map(func(r rune) rune {
		switch r {
		case ':', '/', ' ':
			return '.'
		}
		return r
	}, username)
	if local == "" {
		local = "dotvirt"
	}
	return local + "@dotvirt.noreply"
}

// refSegment sanitizes a string into one valid git branch-ref path segment: git
// refs forbid ':', '~', '^', spaces, '?', '*', '[', '\\', and the sequences '..'
// and '@{'; a component also may not end in '.lock' nor be the single char '@'.
// This is for HUMAN READABILITY of the branch only - uniqueness comes from the
// hash proposedBranch appends, so the lossy mapping here can't cause collisions.
func refSegment(s string) string {
	out := strings.Map(func(r rune) rune {
		switch r {
		case ':', '~', '^', ' ', '?', '*', '[', '\\', '/', '@':
			return '-'
		}
		return r
	}, s)
	out = strings.ReplaceAll(out, "..", "-") // '@' is already gone, so '@{' can't occur
	out = strings.TrimSuffix(out, ".lock")
	out = strings.Trim(out, "-.")
	if out == "" {
		out = "x"
	}
	return out
}

// shortHash is a stable 10-hex-char fingerprint of the exact (user, project),
// appended to the working branch so distinct identities get distinct branches
// even when refSegment maps their readable forms to the same string.
func shortHash(user, project string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + project))
	return hex.EncodeToString(sum[:])[:10]
}

// requireDNS1123 is validate.RequireDNS1123 wrapped with ErrInvalid so the
// transport maps a bad name to a 400.
func requireDNS1123(field, s string) error {
	if err := validate.RequireDNS1123(field, s); err != nil {
		return fmt.Errorf("%w: %s", model.ErrInvalid, err)
	}
	return nil
}
