// Package validate holds the shared trust-boundary checks for values a client
// sends that become Kubernetes metadata, manifest fields, or repo file paths.
// Each refuses with model.ErrInvalid naming the field, so a value the API
// server would reject at apply fails at the form instead of after merge.
package validate

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/api/validate/content"

	"github.com/epheo/dotvirt/internal/model"
)

// dns1123Label matches a Kubernetes DNS-1123 label - lowercase alphanumerics and
// '-', not leading or trailing. Names crossing this gate become metadata.name,
// label values, and git file paths, so it is the one defense against path
// traversal ("../x"), separators ("a/b"), and names k8s would reject at apply.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// DNS1123Name reports whether s is a DNS-1123 label (max 63 chars).
func DNS1123Name(s string) bool {
	return len(s) > 0 && len(s) <= 63 && dns1123Label.MatchString(s)
}

// RequireDNS1123 refuses s as the caller's input (model.ErrInvalid) when it
// fails DNS1123Name, naming field.
func RequireDNS1123(field, s string) error {
	if !DNS1123Name(s) {
		return fmt.Errorf("%w: %s %q must be a DNS-1123 label (lowercase alphanumeric and -, max 63)", model.ErrInvalid, field, s)
	}
	return nil
}

// RequireDNS1123List is RequireDNS1123 over every name in names.
func RequireDNS1123List(field string, names []string) error {
	for _, n := range names {
		if err := RequireDNS1123(field, n); err != nil {
			return err
		}
	}
	return nil
}

// RequireObjectName refuses s unless it is a DNS-1123 subdomain: the
// metadata.name rule for most kinds (nodes, storage classes, instance types).
func RequireObjectName(field, s string) error {
	return check(field, s, content.IsDNS1123Subdomain(s))
}

// RequireLabels refuses a label map whose keys or values the API server would
// reject, e.g. a selector or a label set.
func RequireLabels(field string, labels map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		if err := RequireLabelKey(field, k); err != nil {
			return err
		}
		if err := check(field+" "+k+" value", labels[k], content.IsLabelValue(labels[k])); err != nil {
			return err
		}
	}
	return nil
}

// RequireLabelKey refuses k unless it is a valid label key: a name with an
// optional DNS-subdomain prefix.
func RequireLabelKey(field, k string) error {
	return check(field+" key", k, content.IsLabelKey(k))
}

// RequireQuantity refuses s unless it is a positive Kubernetes quantity
// (e.g. 4Gi).
func RequireQuantity(field, s string) error {
	q, err := resource.ParseQuantity(s)
	if err != nil || q.Sign() <= 0 {
		return fmt.Errorf("%w: %s %q must be a positive quantity (e.g. 4Gi)", model.ErrInvalid, field, s)
	}
	return nil
}

// RequireOneOf refuses s unless it is one of allowed.
func RequireOneOf(field, s string, allowed ...string) error {
	if !slices.Contains(allowed, s) {
		quoted := make([]string, len(allowed))
		for i, a := range allowed {
			quoted[i] = fmt.Sprintf("%q", a)
		}
		return fmt.Errorf("%w: %s %q must be one of %s", model.ErrInvalid, field, s, strings.Join(quoted, ", "))
	}
	return nil
}

func check(field, s string, problems []string) error {
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s %q: %s", model.ErrInvalid, field, s, strings.Join(problems, "; "))
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

// RequireRepoPath refuses s as the caller's input (model.ErrInvalid) when it
// fails RepoPath, naming field.
func RequireRepoPath(field, s string) error {
	if !RepoPath(s) {
		return fmt.Errorf("%w: %s %q must be a clean repo-relative path", model.ErrInvalid, field, s)
	}
	return nil
}
