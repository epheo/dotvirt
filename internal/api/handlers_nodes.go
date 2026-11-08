package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
)

// Node maintenance (the By-Node view): read a node's cordon/maintenance state,
// toggle cordon, enter/exit maintenance mode. Nodes are cluster-scoped, so
// these don't go through resolveProject: the apiserver checks each patch under
// the caller's own token, on the question NodeInfo.CanCordon asks for the UI.

// handleNodes lists the virtualization hosts (KubeVirt-schedulable nodes) as
// live-migration target candidates. A caller without node-list RBAC gets 403,
// and the migrate dialog offers only scheduler-picked placement.
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	_, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	nodes, err := c.ListNodes(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, nodes)
}

// handleNodeInfo returns a node's schedulability + whether the caller may cordon it.
func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	node := r.PathValue("node")
	info, err := c.NodeInfo(r.Context(), node)
	if err != nil {
		fail(w, err)
		return
	}
	info.CanCordon = s.capable(r.Context(), id, c, nodeAction(node))
	writeJSON(w, info)
}

// handleNodeCordon patches node.spec.unschedulable (cordon/uncordon).
func (s *Server) handleNodeCordon(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	req, ok := decode[struct {
		Unschedulable bool `json:"unschedulable"`
	}](w, r)
	if !ok {
		return
	}
	verb := "Cordon"
	if !req.Unschedulable {
		verb = "Uncordon"
	}
	opErr := c.SetNodeCordon(r.Context(), r.PathValue("node"), req.Unschedulable)
	s.recordTask(verb, "", r.PathValue("node"), id.Username, opErr == nil)
	if opErr != nil {
		fail(w, opErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNodeMaintenance enters or exits maintenance mode (annotation + cordon
// in one node patch). Evacuation is a separate call (handleNodeEvacuate) so a
// failed sweep can be retried without re-patching the node.
func (s *Server) handleNodeMaintenance(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	req, ok := decode[struct {
		Enter bool `json:"enter"`
	}](w, r)
	if !ok {
		return
	}
	verb := "Enter maintenance"
	if !req.Enter {
		verb = "Exit maintenance"
	}
	opErr := c.SetNodeMaintenance(r.Context(), r.PathValue("node"), req.Enter)
	s.recordTask(verb, "", r.PathValue("node"), id.Username, opErr == nil)
	if opErr != nil {
		fail(w, opErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNodeEvacuate live-migrates every running VM off the node - in a dotvirt
// project or not. The candidate list comes from the SA snapshot, the only complete
// view of a node (the caller's inventory holds just their projects' VMs, which is
// how untracked VMs used to be stranded); each migrate still runs under the
// caller's own token, so per-VM RBAC gates every move and failures surface as
// rows instead of silent omissions. Gated on the node-patch authority that also
// reveals the maintenance panel; a failure names its VM only when the caller may
// read VMs in its namespace, so the sweep never enumerates what RBAC hides.
func (s *Server) handleNodeEvacuate(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	node := r.PathValue("node")
	if !s.gate(w, r, scope{id: id, cluster: c}, nodeAction(node)) {
		return
	}
	if _, err := c.NodeInfo(r.Context(), node); err != nil {
		fail(w, err)
		return
	}
	out := model.Evacuation{Failures: []model.EvacuationFailure{}}
	for ns, vms := range s.state.Live() {
		for name, live := range vms {
			if live.NodeName != node || live.Phase != "Running" {
				continue
			}
			if m := live.Migration; m != nil && !m.Completed && !m.Failed {
				out.Skipped++ // a second migrate on an active move would 409
				continue
			}
			mErr := c.Migrate(r.Context(), ns, name, "")
			s.recordTask("Live-migration", ns, name, id.Username, mErr == nil)
			if mErr != nil {
				out.Failures = append(out.Failures, model.EvacuationFailure{Namespace: ns, Name: name, Error: mErr.Error()})
				continue
			}
			out.Requested++
		}
	}
	out.Failures, out.Hidden = s.readableFailures(r.Context(), id, c, out.Failures)
	sort.Slice(out.Failures, func(i, j int) bool {
		a, b := out.Failures[i], out.Failures[j]
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	writeJSON(w, out)
}

// readableFailures keeps the evacuation failures in namespaces whose VMs the
// caller may read, and counts the rest: a sweep must not read as complete, nor
// name a VM RBAC hides.
func (s *Server) readableFailures(ctx context.Context, id auth.Identity, c *cluster.Client, failures []model.EvacuationFailure) ([]model.EvacuationFailure, int) {
	readable := map[string]bool{}
	kept := make([]model.EvacuationFailure, 0, len(failures))
	hidden := 0
	for _, f := range failures {
		ok, asked := readable[f.Namespace]
		if !asked {
			ok = s.capable(ctx, id, c, vmAction(verbList, f.Namespace))
			readable[f.Namespace] = ok
		}
		if ok {
			kept = append(kept, f)
		} else {
			hidden++
		}
	}
	return kept, hidden
}
