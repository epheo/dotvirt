// Package export keeps dotvirt's running branch in sync with the live cluster:
// it lists VMs, serializes them deterministically, and commits any change to the
// running branch on an interval. dotvirt owns this branch; users never edit it.
package export

import (
	"context"
	"log"
	"time"

	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/git"
	kubevirtcorev1 "kubevirt.io/api/core/v1"
)

// Exporter snapshots cluster VM state onto the running branch.
type Exporter struct {
	cluster *cluster.Client
	repo    *git.WriteRepo
	branch  string
}

// New builds an Exporter writing to the given running branch.
func New(c *cluster.Client, repo *git.WriteRepo, runningBranch string) *Exporter {
	return &Exporter{cluster: c, repo: repo, branch: runningBranch}
}

// Once performs a single export: read live VMs, serialize, commit to running if
// anything changed. Returns whether a commit was made.
func (e *Exporter) Once(ctx context.Context) (bool, error) {
	vms, err := e.cluster.ListVMObjects(ctx)
	if err != nil {
		return false, err
	}
	if len(vms) == 0 {
		// Not an error, but worth surfacing: usually means no namespaces match
		// the project label selector, so dotvirt is managing nothing.
		log.Printf("export: no VMs found in scoped namespaces; nothing to sync")
		return false, nil
	}

	files, err := manifestsFor(vms)
	if err != nil {
		return false, err
	}

	res, err := e.repo.Commit(e.branch, "dotvirt: sync running state from cluster", files)
	if err != nil {
		return false, err
	}
	return res.Committed, nil
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

// Run exports once immediately, then every interval until ctx is cancelled.
// Errors are logged and retried on the next tick rather than stopping the loop.
func (e *Exporter) Run(ctx context.Context, interval time.Duration) {
	tick := func() {
		committed, err := e.Once(ctx)
		switch {
		case err != nil:
			log.Printf("export: %v", err)
		case committed:
			log.Printf("export: committed updated running state to %q", e.branch)
		}
	}

	tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
