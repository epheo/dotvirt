package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/netstate"
)

// handleTrace simulates one flow through the policy planes - served entirely
// from the SA snapshots, like the effective-policy answer. The caller must be
// able to see both in-cluster ends; resolveProject gates each namespace.
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[model.TraceRequest](w, r)
	if !ok {
		return
	}
	if req.Source.Namespace == "" || req.Source.VM == "" {
		fail(w, invalid(errors.New("source namespace and vm are required")))
		return
	}
	dstVM := req.Destination.Namespace != "" && req.Destination.VM != ""
	if dstVM == (req.Destination.IP != "") {
		fail(w, invalid(errors.New("destination must be a vm or an ip")))
		return
	}
	if req.Destination.IP != "" {
		if _, err := netip.ParseAddr(req.Destination.IP); err != nil {
			fail(w, invalid(errors.New("invalid destination ip")))
			return
		}
	}
	switch req.Protocol {
	case "", "TCP", "UDP", "SCTP":
	default:
		fail(w, invalid(errors.New("protocol must be TCP, UDP or SCTP")))
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		fail(w, invalid(errors.New("invalid port")))
		return
	}

	sc, ok := s.resolveProject(w, r, byNamespace(req.Source.Namespace))
	if !ok {
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
		fail(w, fmt.Errorf("%w: source vm", model.ErrNotFound))
		return
	}
	var dst *netstate.TraceWorkload
	if dstVM {
		d, ok := s.traceWorkload(req.Destination.Namespace, req.Destination.VM)
		if !ok {
			fail(w, fmt.Errorf("%w: destination vm", model.ErrNotFound))
			return
		}
		dst = &d
	}

	res := s.netstate.Trace(src, dst, req.Destination.IP, req.Protocol, req.Port)
	can := s.clusterAuthority(r.Context(), sc.id, sc.cluster)
	for i := range res.Steps {
		if p := res.Steps[i].Policy; p != nil {
			if s.drift != nil {
				s.policyDrift(p)
			}
			redactSubjects(can, p)
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
	podNet, defaultNet, nets, ips, _ := s.state.WorkloadNetworks(ns, name)
	wl := netstate.TraceWorkload{
		Namespace: ns, Name: name,
		PodLabels: lbls, IPs: ips, PodNet: podNet, DefaultNet: defaultNet, Nets: nets,
	}
	for _, n := range s.state.Namespaces() {
		if n.Name == ns {
			wl.NSLabels = n.Labels
			break
		}
	}
	return wl, true
}
