package api

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// Every create authority the routes gate on must be a kind the table lists:
// the gate and the apply-time AppProject boundary are spelled from one place.
func TestEverySSARIsATableKind(t *testing.T) {
	refs := map[string]ssarRef{
		"cudn": ssarCUDN, "uplink": ssarUplink, "namespace": ssarNamespace, "egressip": ssarEgressIP,
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
	for _, r := range platformAuthorResources {
		if r.resource == "" {
			t.Errorf("platform authoring signal %+v is empty", r)
		}
	}
}
