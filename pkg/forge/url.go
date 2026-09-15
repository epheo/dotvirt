// Repo-URL helpers: parsing owner/repo out of clone URLs and canonicalizing
// the forms the same repo is written in.

package forge

import (
	"fmt"
	"net/url"
	"strings"
)

// parsed is a URL split once by net/url. origin is "scheme://[user@]host" and
// path what follows it, as written; a host-free ref ("owner/repo.git") or a
// string net/url refuses has no origin and is all path.
type parsed struct {
	origin string
	host   string // lowercased; credentials dropped, since user@host and host are one forge
	path   string
}

func parseURL(raw string) parsed {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil {
		return parsed{path: s}
	}
	if u.Scheme == "" {
		return parsed{path: u.Path}
	}
	origin := u.Scheme + "://"
	if u.User != nil {
		origin += u.User.String() + "@"
	}
	return parsed{origin: origin + u.Host, host: strings.ToLower(u.Host), path: u.Path}
}

// ownerRepo extracts the owner and repo from a Forgejo/Gitea repo URL. It takes
// the last two path segments and strips a trailing ".git", so
// https://forge.example/dotvirt/team-a.git -> ("dotvirt", "team-a"). It fails
// closed (ok=false) on anything it can't parse cleanly, so the caller degrades to
// a compare link rather than building a malformed API path.
func ownerRepo(repoURL string) (owner, repo string, ok bool) {
	p := strings.TrimSuffix(strings.Trim(parseURL(repoURL).path, "/"), ".git")
	parts := strings.Split(p, "/")
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
// same repo written three ways - the forge clone_url (....git), the html_url (no
// .git), and a trailing-slash annotation - resolve to one key, so a push webhook
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
// URL - the prefix Argo longest-prefix-matches to attach one repo-credential to
// every repo under that owner, and what a sibling repo under the same owner is
// named from. Returns the input unchanged when there's no repo segment to strip.
func OwnerPrefixURL(repoURL string) string {
	u := parseURL(repoURL)
	p := strings.TrimSuffix(strings.TrimRight(u.path, "/"), ".git")
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return repoURL
	}
	return u.origin + p[:i]
}

// CompareURL is the browser URL to manually open a PR for head->base, used when
// the forge API isn't configured.
func (c *Client) CompareURL(head, base string) string {
	return fmt.Sprintf("%s/%s/%s/compare/%s...%s", c.f.baseURL, c.owner, c.repo, base, head)
}

// urlPath returns the path component of a URL, or raw unchanged if it has none -
// enough to identify a webhook across a host change without binding to scheme/host/port.
func urlPath(raw string) string {
	if p := parseURL(raw).path; p != "" {
		return p
	}
	return raw
}

// PathRef: a repo URL's host-free path form ("owner/repo.git"), what annotations
// carry. The forge identity then lives only in the install config, so a host
// change re-resolves projects instead of stranding them. Relative input passes
// through.
func PathRef(repoURL string) string {
	u := parseURL(repoURL)
	if u.origin != "" && u.path == "" {
		return repoURL
	}
	return strings.TrimLeft(u.path, "/")
}

// ResolveRef joins a relative ref onto base; absolute refs pass through.
// Empty base leaves a relative ref unresolvable: "".
func ResolveRef(base, ref string) string {
	if parseURL(ref).origin != "" {
		return ref
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(ref, "/")
}
