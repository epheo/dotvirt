package api

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// Every create authority the routes gate on must be a kind the table lists:
// the gate and the apply-time AppProject boundary are spelled from one place.
func TestEverySSARIsATableKind(t *testing.T) {
	refs := map[string]ssarRef{
		"cudn": ssarCUDN, "namespace": ssarNamespace, "egressip": ssarEgressIP,
		"externalroute": ssarExtRoute, "anp": ssarANP, "banp": ssarBANP, "descheduler": ssarDescheduler,
		"machineconfig": ssarMachineCfg, "template": ssarVMTemplate,
	}
	for name, ref := range refs {
		found := false
		for _, r := range model.Resources() {
			for _, k := range r.Kinds {
				if ssarFor(k) == ref {
					found = true
				}
			}
		}
		if !found || ref.resource == "" {
			t.Errorf("ssar %s = %+v is not in the kind table", name, ref)
		}
	}
	listed := map[ssarRef]bool{}
	for _, q := range platformListing() {
		if q.Verb != verbList || q.Resource == "" || q.Namespace != "" {
			t.Errorf("platform listing question %+v is not a cluster-wide list", q)
		}
		listed[ssarRef{q.Group, q.Resource}] = true
	}
	for _, ref := range []ssarRef{ssarNamespace, ssarCUDN, ssarUplink, ssarEgressIP, ssarExtRoute, ssarANP, ssarBANP, ssarMachineCfg} {
		if !listed[ref] {
			t.Errorf("reading the platform repo does not ask list on %s", ref.resource)
		}
	}
}
