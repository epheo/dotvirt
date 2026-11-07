package changeset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/model"
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

// invalid classifies a renderer's refusal as the caller's input. A validate
// error already carries the kind and passes through, so the message is never
// prefixed twice.
func invalid(err error) error {
	if errors.Is(err, model.ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: %v", model.ErrInvalid, err)
}

// staleEdit is a field edit the base branch no longer takes: base moved
// after stage, or the edit was never dry-run. A conflict, whose way out
// depends on where the edit lives (see proposeRun.extend).
type staleEdit struct {
	entry draft.Entry
	base  string
	err   error
}

// what names the edit and why it fails, with err's own kind dropped so the
// message names one kind once.
func (s *staleEdit) what() string {
	lines := strings.Split(s.err.Error(), "\n")
	for i, l := range lines {
		for _, kind := range []error{model.ErrInvalid, model.ErrConflict} {
			l = strings.TrimPrefix(l, kind.Error()+": ")
		}
		lines[i] = l
	}
	return fmt.Sprintf("the edit of %s no longer applies to %s (%s)", objectLabel(s.entry), s.base, strings.Join(lines, "; "))
}

func (s *staleEdit) Error() string {
	return model.ErrConflict.Error() + ": " + s.what() + "; unstage it and stage it again on the current version"
}

func (s *staleEdit) Is(target error) bool { return target == model.ErrConflict }
