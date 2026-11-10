package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"

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
	if !ok || !s.gateVMRead(w, r, sc, req.Source.Namespace, req.Source.VM, ssarVMI) {
		return
	}
	if dstVM {
		if _, ok := s.resolveProject(w, r, byNamespace(req.Destination.Namespace)); !ok {
			return
		}
		if !s.gateVMRead(w, r, sc, req.Destination.Namespace, req.Destination.VM, ssarVMI) {
			return
		}
	}
	if s.netstate == nil {
		writeJSON(w, model.TraceResult{Steps: []model.TraceStep{}})
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

	var view *netstate.TraceView
	if dst == nil {
		view = s.traceView(r, sc)
	}
	res := s.netstate.Trace(view, src, dst, req.Destination.IP, req.Protocol, req.Port)
	pr := s.policyRedactor(r.Context(), sc.id, sc.cluster)
	for i := range res.Steps {
		if p := res.Steps[i].Policy; p != nil {
			if s.drift != nil {
				s.policyDrift(p)
			}
			pr.apply(p, res.Steps[i].Rule)
		}
	}
	writeJSON(w, res)
}

// traceWorkload assembles one endpoint from the clusterstate snapshot: the
// same identity the effective-policy answer matches, plus its namespace labels.
func (s *Server) traceWorkload(ns, name string) (netstate.TraceWorkload, bool) {
	w, found := s.state.Workload(ns, name)
	if !found {
		return netstate.TraceWorkload{}, false
	}
	return netstate.TraceWorkload{
		Namespace: ns, Name: name,
		PodLabels: w.Labels, IPs: w.IPs, PodNet: w.PodNet, DefaultNet: w.DefaultNet, Nets: w.Nets,
		NSLabels: s.state.NamespaceLabels(ns),
	}, true
}

// traceView is what the caller may read of the tenant-configured networks a
// bare address is placed against: the namespaces they see, every shared
// network when their catalog lists them all, and the VMs in those
// namespaces. The shared-network reviews are the cached ones /api/networks
// makes, asked for every bare-address trace alike. A failed visibility read
// sees no namespace.
func (s *Server) traceView(r *http.Request, sc scope) *netstate.TraceView {
	ctx := r.Context()
	v, _ := s.viewFor(ctx, sc.id, sc.cluster)
	visible := v.names
	return &netstate.TraceView{
		NADs:   func(ns string) bool { return visible[ns] && v.lists(ssarFor(model.KindNAD), ns) },
		UDNs:   func(ns string) bool { return visible[ns] && v.lists(ssarFor(model.KindUDN), ns) },
		Shared: s.seesAllNetworks(ctx, sc.id, sc.cluster),
		Workloads: func(addr netip.Addr) []netstate.TraceWorkload {
			var out []netstate.TraceWorkload
			live := s.state.Live()
			for ns := range visible {
				if !v.lists(ssarVM, ns) || !v.lists(ssarVMI, ns) {
					continue
				}
				for name, vm := range live[ns] {
					if slices.ContainsFunc(vm.IPs, func(ip string) bool {
						a, err := netip.ParseAddr(ip)
						return err == nil && a.Unmap() == addr
					}) {
						if w, ok := s.traceWorkload(ns, name); ok {
							out = append(out, w)
						}
					}
				}
			}
			return out
		},
	}
}
