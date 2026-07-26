package changeset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/vmgen"
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
// This is for HUMAN READABILITY of the branch only — uniqueness comes from the
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

// changesForCreate renders a new-VM spec as "add" semantic items for the draft
// preview, without showing YAML.
func changesForCreate(s vmgen.Spec) []model.Change {
	out := []model.Change{
		{Field: "Create VM", Action: "add", To: s.Namespace + "/" + s.Name},
		{Field: "Instance type", Action: "add", To: s.Instancetype},
		{Field: "Preference", Action: "add", To: s.Preference},
		{Field: "OS image", Action: "add", To: s.OSImage.Name},
	}
	if s.DiskSize != "" {
		out = append(out, model.Change{Field: "Root disk", Action: "add", To: s.DiskSize})
	}
	if s.StorageClass != "" {
		out = append(out, model.Change{Field: "Storage class", Action: "add", To: s.StorageClass})
	}
	for _, d := range s.ExtraDisks {
		out = append(out, model.Change{Field: "Disk", Action: "add", To: diskLabel(d.Name, d.Size, d.StorageClass)})
	}
	for _, n := range s.Networks {
		out = append(out, model.Change{Field: "Network", Action: "add", To: n.Name})
	}
	if s.PrimaryNetwork != nil && !*s.PrimaryNetwork {
		out = append(out, model.Change{Field: "Primary network", Action: "remove", From: "VM Network"})
	}
	out = append(out, model.Change{Field: "Power", Action: "add", To: powerWord(s.Running)})
	return out
}

func powerWord(running bool) string {
	if running {
		return "On"
	}
	return "Off"
}

// diskLabel renders an added disk for the changeset preview, appending the
// storage class only when one was chosen (empty = cluster default).
func diskLabel(name, size, class string) string {
	if class != "" {
		return fmt.Sprintf("%s (%s, %s)", name, size, class)
	}
	return fmt.Sprintf("%s (%s)", name, size)
}

