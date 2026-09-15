package draft

import (
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

// The on-disk vocabulary and the kind table are two spellings of one set: a
// resource added on either side without the other would stage entries no view
// can label or locate.
func TestEveryResourceHasATableRow(t *testing.T) {
	consts := []Resource{
		ResourceVM, ResourceNetwork, ResourceUplink, ResourceNamespace, ResourceRoleBinding,
		ResourceEgressFirewall, ResourceEgressIP, ResourceExternalRoute, ResourceNetworkPolicy,
		ResourceAdminNetworkPolicy, ResourceBaselineAdminNetworkPolicy, ResourceDRS, ResourceTemplate,
	}
	if len(consts) != len(model.Resources()) {
		t.Errorf("%d draft resources, %d table rows", len(consts), len(model.Resources()))
	}
	for _, r := range consts {
		if _, ok := model.LookupResource(string(r)); !ok {
			t.Errorf("%s has no table row", r)
		}
		if r.CreateLabel() == "" || r.EditLabel() == "" || len(r.Kinds()) == 0 {
			t.Errorf("%s: labels %q/%q kinds %v", r, r.CreateLabel(), r.EditLabel(), r.Kinds())
		}
	}
	if Resource("").Kinds()[0] != "VirtualMachine" {
		t.Error("the empty resource must read as the VM")
	}
}
