// Package deps probes the cluster for the operators dotvirt relies on, so the
// installer can GATE on hard prerequisites and DEGRADE on soft ones with a clear
// status condition instead of failing opaquely.
//
// ArgoCD (OpenShift GitOps or community Argo CD) is a hard PREREQUISITE the dotvirt
// operator never installs: it's a cluster-singleton orgs run and manage centrally,
// and dotvirt's whole apply path rides it. KubeVirt is hard too (no VMs without it).
// The networking tier's OVN-K + NMState — and CDI for disk import — are SOFT: when
// absent, dotvirt simply hides those affordances (it already degrades at runtime).
package deps

import (
	"strings"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// Dependency is a CRD-providing operator dotvirt needs, identified by an API group
// and a representative resource. Hard deps block the install; soft deps gate a
// feature (Enables names what's lost when one is absent).
type Dependency struct {
	Label    string
	Group    string
	Resource string
	Hard     bool
	Enables  string
}

// Required is the full dependency set, in report order.
var Required = []Dependency{
	{Label: "OpenShift GitOps / Argo CD", Group: "argoproj.io", Resource: "applications", Hard: true},
	{Label: "KubeVirt / OpenShift Virtualization", Group: "kubevirt.io", Resource: "virtualmachines", Hard: true},
	{Label: "CDI", Group: "cdi.kubevirt.io", Resource: "datavolumes", Enables: "disk import + image upload"},
	{Label: "OVN-Kubernetes UDN", Group: "k8s.ovn.org", Resource: "userdefinednetworks", Enables: "the networking tier (port groups)"},
	{Label: "NMState", Group: "nmstate.io", Resource: "nodenetworkconfigurationpolicies", Enables: "uplinks / physical adapters"},
}

// Result is the probe outcome.
type Result struct {
	Present     map[string]bool // Dependency.Label -> present
	MissingHard []string
	MissingSoft []string
}

// Probe reports which dependencies' CRDs the cluster currently serves. A partial
// discovery error (one unavailable aggregated apiservice) is tolerated — we
// classify from whatever the API server did return.
func Probe(cfg *rest.Config) (Result, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return Result{}, err
	}
	_, lists, err := dc.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return Result{}, err
	}

	served := map[string]bool{} // "group/resource"
	for _, l := range lists {
		group := l.GroupVersion // "group/version", or just "version" for the core group
		if i := strings.IndexByte(group, '/'); i >= 0 {
			group = group[:i]
		} else {
			group = ""
		}
		for _, r := range l.APIResources {
			served[group+"/"+r.Name] = true
		}
	}

	res := Result{Present: map[string]bool{}}
	for _, d := range Required {
		ok := served[d.Group+"/"+d.Resource]
		res.Present[d.Label] = ok
		switch {
		case ok:
		case d.Hard:
			res.MissingHard = append(res.MissingHard, d.Label)
		default:
			res.MissingSoft = append(res.MissingSoft, d.Label+" ("+d.Enables+")")
		}
	}
	return res, nil
}

// Summary is a one-line status suitable for a condition message.
func (r Result) Summary() string {
	switch {
	case len(r.MissingHard) > 0:
		return "missing required prerequisite(s): " + strings.Join(r.MissingHard, ", ")
	case len(r.MissingSoft) > 0:
		return "satisfied; degraded (absent: " + strings.Join(r.MissingSoft, ", ") + ")"
	default:
		return "all dependencies present"
	}
}
