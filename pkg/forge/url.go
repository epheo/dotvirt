// Repo-URL helpers: parsing owner/repo out of clone URLs and canonicalizing
// the forms the same repo is written in.

package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// ownerRepo extracts the owner and repo from a Forgejo/Gitea repo URL. It takes
// the last two path segments and strips a trailing ".git", so
// https://forge.example/dotvirt/team-a.git → ("dotvirt", "team-a"). It fails
// closed (ok=false) on anything it can't parse cleanly, so the caller degrades to
// a compare link rather than building a malformed API path.
func ownerRepo(repoURL string) (owner, repo string, ok bool) {
	s := repoURL
	// Drop any query string / fragment before touching the path or the .git suffix.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	// Drop scheme + host: keep the path.
	if i := strings.Index(s, "://"); i >= 0 {
		if slash := strings.IndexByte(s[i+3:], '/'); slash >= 0 {
			s = s[i+3+slash+1:]
		} else {
			return "", "", false
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	owner, repo = parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// NormalizeRepoURL canonicalizes a git repo URL for equality comparison: trimmed,
// no trailing slash, a single trailing ".git" stripped, lowercased. It lets the
// same repo written three ways — the forge clone_url (…​.git), the html_url (no
// .git), and a trailing-slash annotation — resolve to one key, so a push webhook
// reliably finds the repo's poller (RepoSet) and its managing ArgoCD Application
// (argo.Snapshot.RefreshForRepo). Returns "" for an empty/blank input.
func NormalizeRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	// Lowercase first so a mixed-case ".GIT" suffix or host still canonicalizes.
	u = strings.ToLower(u)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return u
}

// OwnerPrefixURL is the forge owner URL ("scheme://host/.../<owner>") of a repo
// URL — the prefix Argo longest-prefix-matches to attach one repo-credential to
// every repo under that owner. Unlike path.Dir, it preserves the "://" in the
// scheme (path.Dir collapses it to ":/", yielding a prefix Argo never matches).
// Returns the input unchanged when there's no repo segment to strip.
func OwnerPrefixURL(repoURL string) string {
	s := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
	// Find where the path starts, after scheme://host, so we never cut into "://".
	pathStart := 0
	if i := strings.Index(s, "://"); i >= 0 {
		pathStart = i + 3
	}
	slash := strings.LastIndexByte(s[pathStart:], '/')
	if slash <= 0 {
		return repoURL // no owner/repo path segments to strip
	}
	return s[:pathStart+slash]
}

// CompareURL is the browser URL to manually open a PR for head→base, used when
// the forge API isn't configured.
func (c *Client) CompareURL(head, base string) string {
	return fmt.Sprintf("%s/%s/%s/compare/%s...%s", c.baseURL, c.owner, c.repo, base, head)
}

// urlPath returns the path component of a URL, or raw unchanged if it doesn't parse —
// enough to identify a webhook across a host change without binding to scheme/host/port.
func urlPath(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		return u.Path
	}
	return raw
}

// PathRef reduces a repo URL to its host-free path form ("owner/repo.git"), the
// shape dotvirt stamps into dotvirt.io/repo annotations: the forge's identity then
// lives ONLY in the install config, so a forge-host change (reinstall, apps-domain
// move) re-resolves every project instead of stranding them on a dead absolute URL.
// Already-relative input passes through; a URL with no path is returned unchanged.
func PathRef(repoURL string) string {
	s := strings.TrimSpace(repoURL)
	i := strings.Index(s, "://")
	if i < 0 {
		return strings.TrimLeft(s, "/")
	}
	s = s[i+3:]
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return repoURL
	}
	return s[slash+1:]
}

// ResolveRef joins a PathRef-style relative ref onto a forge base URL; an
// absolute ref is returned unchanged. Empty base leaves a relative ref
// unresolvable, signalled by returning "".
func ResolveRef(base, ref string) string {
	if strings.Contains(ref, "://") {
		return ref
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(ref, "/")
}
