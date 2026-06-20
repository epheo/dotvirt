// Package export keeps each project's running branch in sync with the live
// cluster: it reads projects and their VM objects from the SA-owned snapshot,
// serializes them deterministically, and commits any change to that project's
// repo's running branch. dotvirt owns these branches; users never edit them. All
// work runs under the SA identity — export has no user context — and a tick
// touches the cluster zero times (the reflectors already hold the VM objects).
package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sort"
	"time"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/project"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

// Exporter snapshots each project's live VM state onto that project's running
// branch, reading from the SA snapshot and writing via the per-project RepoSet.
type Exporter struct {
	state    *clusterstate.State // SA snapshot: project topology + full VM objects
	resolver *project.Resolver
	repos    *git.RepoSet
	branch   string // running branch name (same across projects)

	// lastSig is the content signature of the last successful export per repo, so a
	// tick whose live VM set is unchanged skips the (network) clone entirely.
	lastSig map[string]string
}

// New builds an Exporter over the SA snapshot, whose project-labeled namespaces
// drive which projects to export and whose VM store supplies the objects.
func New(state *clusterstate.State, resolver *project.Resolver, repos *git.RepoSet, runningBranch string) *Exporter {
	return &Exporter{state: state, resolver: resolver, repos: repos, branch: runningBranch, lastSig: map[string]string{}}
}

// Once exports every resolved project once: for each project with a repo, write
// its namespaces' live VMs to its repo's running branch. A per-project failure is
// logged and skipped so the others still sync (the loop never aborts, hence no
// returned error). Returns how many projects' running branches it committed an
// update to. Topology comes from the shared snapshot with no visible-namespace
// filter (the SA export sees every project).
func (e *Exporter) once() int {
	// Exporting prunes manifests absent from the snapshot, so a half-filled
	// snapshot (reflectors still on their initial LIST) would read as mass VM
	// deletion. Gate on exactly the stores the export reads — VMs + namespaces —
	// NOT full Synced(): a permanently-failing VMI reflector (e.g. a removed RBAC
	// verb) must not silently wedge export for every project forever. Skip the tick
	// until ready; a stale running branch beats a wrong one.
	if !e.state.ExportReady() {
		log.Printf("export: VM/namespace snapshot not ready yet; skipping this tick (if this persists, a reflector's initial LIST is failing — check SA RBAC)")
		return 0
	}
	projects := e.resolver.Resolve(e.state.Namespaces(), nil)
	committed := 0
	for _, p := range projects {
		if p.Repo == "" {
			continue // unannotated/conflicting project: nothing to export to
		}
		ok, err := e.exportProject(p)
		if err != nil {
			log.Printf("export %s: %v", p.Name, err)
			continue
		}
		if ok {
			committed++
			log.Printf("export: committed running state for %q -> %s", p.Name, p.Repo)
		}
	}
	return committed
}

func (e *Exporter) exportProject(p project.ProjectInfo) (bool, error) {
	vms := e.state.VMObjects(p.Namespaces)
	_, write, err := e.repos.Get(p.Repo)
	if err != nil {
		return false, err
	}
	files, err := manifestsFor(vms)
	if err != nil {
		return false, err
	}
	// Skip the (network) clone + tree walk when the live VM set is byte-for-byte
	// what we last exported to this repo: the running branch is dotvirt-owned, so an
	// unchanged file set means an unchanged branch. The signature also covers the
	// managed namespaces, so a namespace coming/going still triggers a real export.
	sig := exportSignature(files, p.Namespaces)
	if e.lastSig[p.Repo] == sig {
		return false, nil
	}
	// Manage exactly the project's namespace directories: a VM removed from the
	// cluster leaves no file in `files`, and Commit prunes its now-stale manifest
	// under <namespace>/ so the running branch mirrors live state (an emptied
	// project's namespace dirs are cleared). Non-namespace files (README) are kept.
	res, err := write.Commit(e.branch, "dotvirt: sync running state from cluster", files, p.Namespaces)
	if err != nil {
		return false, err
	}
	e.lastSig[p.Repo] = sig // commit succeeded (or was a no-op): this state is now exported
	return res.Committed, nil
}

// exportSignature is a deterministic fingerprint of the file set + managed dirs,
// so an unchanged tick can skip the clone. Paths are sorted for stability.
func exportSignature(files []git.File, managedDirs []string) string {
	sorted := append([]git.File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, d := range managedDirs {
		h.Write([]byte("d:" + d + "\n"))
	}
	for _, f := range sorted {
		h.Write([]byte("f:" + f.Path + "\n"))
		h.Write(f.Content)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func manifestsFor(vms []kubevirtcorev1.VirtualMachine) ([]git.File, error) {
	files := make([]git.File, 0, len(vms))
	for i := range vms {
		content, err := cluster.ExportManifest(vms[i])
		if err != nil {
			return nil, err
		}
		files = append(files, git.File{Path: cluster.ExportPath(vms[i]), Content: content})
	}
	return files, nil
}

// Run exports once immediately, then whenever the exported manifest set could have
// moved — VMSpecChanged (a VM spec/generation change or add/remove) or
// NamespaceChanged — and on a periodic backstop tick, until ctx is cancelled. It
// deliberately does NOT wake on LiveChanged: the export reads VM specs + namespace
// membership only (never VMI status), so a VMI heartbeat must not trigger the
// marshal pipeline. The content-signature skip (exportSignature) makes a spurious
// wake a cheap fingerprint compare; the ticker is the missed-event backstop for the
// git-write target (an unwatchable external sink). Per-project failures are logged
// and retried (see once).
func (e *Exporter) Run(ctx context.Context, interval time.Duration, bus *eventbus.Bus) {
	wake, cancel := bus.Subscribe(eventbus.VMSpecChanged, eventbus.NamespaceChanged)
	defer cancel()
	e.once()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			e.once()
		case <-t.C:
			e.once()
		}
	}
}
