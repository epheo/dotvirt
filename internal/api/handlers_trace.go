package api

import (
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
)

// handleTrace simulates one flow through the policy planes — served entirely
// from the SA snapshots, like the effective-policy answer. The caller must be
// able to see both in-cluster ends; resolveProject gates each namespace.
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	var req model.TraceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Source.Namespace == "" || req.Source.VM == "" {
		http.Error(w, "source namespace and vm are required", http.StatusBadRequest)
		return
	}
	dstVM := req.Destination.Namespace != "" && req.Destination.VM != ""
	if dstVM == (req.Destination.IP != "") {
		http.Error(w, "destination must be a vm or an ip", http.StatusBadRequest)
		return
	}
	if req.Destination.IP != "" {
		if _, err := netip.ParseAddr(req.Destination.IP); err != nil {
			http.Error(w, "invalid destination ip", http.StatusBadRequest)
			return
		}
	}
	switch req.Protocol {
	case "", "TCP", "UDP", "SCTP":
	default:
		http.Error(w, "protocol must be TCP, UDP or SCTP", http.StatusBadRequest)
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	if _, ok := s.resolveProject(w, r, byNamespace(req.Source.Namespace)); !ok {
		return
	}
	if dstVM {
		if _, ok := s.resolveProject(w, r, byNamespace(req.Destination.Namespace)); !ok {
			return
		}
	}
	if s.netstate == nil {
		writeJSON(w, http.StatusOK, model.TraceResult{Steps: []model.TraceStep{}})
		return
	}

	src, ok := s.traceWorkload(req.Source.Namespace, req.Source.VM)
	if !ok {
		http.Error(w, "source vm not found", http.StatusNotFound)
		return
	}
	var dst *netstate.TraceWorkload
	if dstVM {
		d, ok := s.traceWorkload(req.Destination.Namespace, req.Destination.VM)
		if !ok {
			http.Error(w, "destination vm not found", http.StatusNotFound)
			return
		}
		dst = &d
	}

	res := s.netstate.Trace(src, dst, req.Destination.IP, req.Protocol, req.Port)
	if s.drift != nil {
		for i := range res.Steps {
			if res.Steps[i].Policy != nil {
				s.policyDrift(res.Steps[i].Policy)
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// traceWorkload assembles one endpoint from the clusterstate snapshot: the
// labels selectors match, NIC attachments and live addresses.
func (s *Server) traceWorkload(ns, name string) (netstate.TraceWorkload, bool) {
	lbls, _, found := s.state.WorkloadLabels(ns, name)
	if !found {
		return netstate.TraceWorkload{}, false
	}
	podNet, nets, ips, _ := s.state.WorkloadNetworks(ns, name)
	wl := netstate.TraceWorkload{
		Namespace: ns, Name: name,
		PodLabels: lbls, IPs: ips, PodNet: podNet, Nets: nets,
	}
	for _, n := range s.state.Namespaces() {
		if n.Name == ns {
			wl.NSLabels = n.Labels
			break
		}
	}
	return wl, true
}
