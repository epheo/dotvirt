package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// handleReplication serves where every copy lives, to a caller whose own token
// may list what the answer shows.
func (s *Server) handleReplication(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	if !s.capable(r.Context(), id, c, replicationRead) {
		fail(w, fmt.Errorf("%w: reading replicated storage's placement needs list on its cluster-scoped kinds", model.ErrForbidden))
		return
	}
	writeJSON(w, s.replication.Overview())
}

// handleProtection serves the standing of the replicated disks in the caller's
// namespaces, narrowed by ?project= or ?namespace=. Any caller may ask; the
// rows name no host.
func (s *Server) handleProtection(w http.ResponseWriter, r *http.Request) {
	sc, nss, ok := s.containerScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.replication.Protection(s.pvcNamespaces(r.Context(), sc.id, sc.cluster, nss)))
}

// pvcNamespaces narrows namespaces to those the caller may list claims in.
// Seeing a namespace means listing its VMs, which says nothing of its claims,
// and every row here is a claim's name and standing.
func (s *Server) pvcNamespaces(ctx context.Context, id auth.Identity, c *cluster.Client, namespaces []string) []string {
	acts := make([]action, len(namespaces))
	for i, ns := range namespaces {
		acts[i] = ask(verbList, ssarClaims, ns)
	}
	var out []string
	for i, ok := range s.capableAll(ctx, id, c, acts) {
		if ok {
			out = append(out, namespaces[i])
		}
	}
	return out
}

// repairCaller is the shared gate of the repairs: asked before anything about
// a volume is read, so a refusal says nothing of what exists.
func (s *Server) repairCaller(w http.ResponseWriter, r *http.Request) (auth.Identity, *cluster.Client, bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return id, nil, false
	}
	if !s.capable(r.Context(), id, c, replicationRepair) {
		fail(w, fmt.Errorf("%w: repairing replicated storage needs patch on its Volumes", model.ErrForbidden))
		return id, nil, false
	}
	return id, c, true
}

// handleResolveSplitBrain names the copy that wins. r-drbd then discards every
// other copy's writes since the split, so the checks here turn a stale click
// into a refusal rather than a patch that r-drbd would have to reject.
func (s *Server) handleResolveSplitBrain(w http.ResponseWriter, r *http.Request) {
	id, c, ok := s.repairCaller(w, r)
	if !ok {
		return
	}
	req, ok := decode[struct {
		Survivor string `json:"survivor"`
	}](w, r)
	if !ok {
		return
	}
	name := r.PathValue("volume")
	v, found := s.replication.Volume(name)
	if !found {
		fail(w, fmt.Errorf("%w: volume %s", model.ErrNotFound, name))
		return
	}
	if !v.SplitBrain {
		fail(w, fmt.Errorf("%w: the copies of %s no longer disagree", model.ErrConflict, name))
		return
	}
	if !slices.ContainsFunc(v.Replicas, func(x model.VolumeReplica) bool { return x.Node == req.Survivor && x.Role == "diskful" }) {
		fail(w, fmt.Errorf("%w: %s holds no copy of %s", model.ErrConflict, req.Survivor, name))
		return
	}
	opErr := c.ResolveSplitBrain(r.Context(), name, req.Survivor)
	s.recordTask("Resolve split brain", "", name, id.Username, opErr == nil)
	if opErr != nil {
		fail(w, opErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEvictHost moves the copies off a host that may not come back, or with
// ?dryRun=true says what it would do. One server answer feeds both, so the
// dialog cannot promise what the move then skips.
func (s *Server) handleEvictHost(w http.ResponseWriter, r *http.Request) {
	id, c, ok := s.repairCaller(w, r)
	if !ok {
		return
	}
	node := r.PathValue("node")
	moving, skipped := s.replication.Evictions(node)
	out := model.ReplicationEvict{Moving: make([]string, 0, len(moving)), Skipped: skipped}
	if r.URL.Query().Get("dryRun") == "true" {
		for _, v := range moving {
			out.Moving = append(out.Moving, v.Name)
		}
		writeJSON(w, out)
		return
	}
	failures := make([]error, len(moving))
	acts := make(chan int)
	var wg sync.WaitGroup
	for range min(authConcurrency, len(moving)) {
		wg.Go(func() {
			for i := range acts {
				// A fresh list: the snapshot's may be shared with other readers.
				failures[i] = c.EvictReplica(r.Context(), moving[i].Name, append(slices.Clone(moving[i].Evicting), node))
			}
		})
	}
	for i, v := range moving {
		if slices.Contains(v.Evicting, node) {
			continue
		}
		acts <- i
	}
	close(acts)
	wg.Wait()
	for i, v := range moving {
		if failures[i] != nil {
			out.Skipped = append(out.Skipped, model.ReplicationSkip{Volume: v.Name, Reason: publicMessage(failures[i])})
			continue
		}
		out.Moving = append(out.Moving, v.Name)
	}
	s.recordTask("Move copies off", "", node, id.Username, len(moving) == len(out.Moving))
	writeJSON(w, out)
}
