// Package validate holds the shared trust-boundary checks for names that become
// both Kubernetes metadata and repo file-path segments.
package validate

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// dns1123Label matches a Kubernetes DNS-1123 label — lowercase alphanumerics and
// '-', not leading or trailing. Names crossing this gate become metadata.name,
// label values, and git file paths, so it is the one defense against path
// traversal ("../x"), separators ("a/b"), and names k8s would reject at apply.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// DNS1123Name reports whether s is a DNS-1123 label (max 63 chars).
func DNS1123Name(s string) bool {
	return len(s) > 0 && len(s) <= 63 && dns1123Label.MatchString(s)
}

// RequireDNS1123 returns a descriptive error naming field when s fails DNS1123Name.
func RequireDNS1123(field, s string) error {
	if !DNS1123Name(s) {
		return fmt.Errorf("%s %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", field, s)
	}
	return nil
}

// RepoPath reports whether s is a clean repo-relative file path: no absolute
// root, no traversal, no redundant segments, no backslashes. Paths crossing
// this gate address files in a project repo's proposal diff.
func RepoPath(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return false
	}
	if path.Clean(s) != s {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}

// RequireRepoPath returns a descriptive error naming field when s fails RepoPath.
func RequireRepoPath(field, s string) error {
	if !RepoPath(s) {
		return fmt.Errorf("%s %q must be a clean repo-relative path", field, s)
	}
	return nil
}
