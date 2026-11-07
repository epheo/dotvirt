package api

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtcorev1 "kubevirt.io/api/core/v1"

	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/model"
)

// The band is the descheduler's real trigger window: exact outside-band
// counts, a floor at 0, AsymmetricLow's below-mean-is-a-target semantics, and
// no band at all for an unknown (hand-edited) threshold.
func TestFoldDRSBand(t *testing.T) {
	workers := func(pcts ...float64) []model.HostWorker {
		ws := make([]model.HostWorker, len(pcts))
		for i, p := range pcts {
			ws[i] = model.HostWorker{Pct: p}
		}
		return ws
	}
	load := model.HostLoad{Mean: 30, Nodes: workers(76, 30, 25, 7)}
	foldDRSBand(&load, "Low")
	if load.Band == nil || load.Band.Low != 20 || load.Band.High != 40 {
		t.Fatalf("band = %+v, want [20,40] around mean 30", load.Band)
	}
	if load.Band.Above != 1 || load.Band.Below != 1 {
		t.Errorf("above/below = %d/%d, want 1/1 (30 and 25 are in-band)", load.Band.Above, load.Band.Below)
	}

	asym := model.HostLoad{Mean: 5, Nodes: workers(2, 4, 20)}
	foldDRSBand(&asym, "AsymmetricLow")
	if asym.Band == nil || asym.Band.Low != 5 || asym.Band.High != 15 {
		t.Fatalf("asymmetric band = %+v, want [5,15]: below-mean already counts as a target", asym.Band)
	}
	if asym.Band.Above != 1 || asym.Band.Below != 2 {
		t.Errorf("asymmetric above/below = %d/%d, want 1/2", asym.Band.Above, asym.Band.Below)
	}

	unknown := model.HostLoad{Mean: 30, Nodes: workers(76, 30, 25, 7)}
	foldDRSBand(&unknown, "hand-edited")
	if unknown.Band != nil {
		t.Errorf("unknown threshold must not fabricate a band, got %+v", unknown.Band)
	}
}

// The phase map derives from kubevirt_vmi_info, which only sees VMIs - a
// stopped VM has none and was invisible ("2 Running" on a 3-VM project, found
// on the live console). foldStoppedVMs adds them under the same lowercase
// key convention the metric-derived entries use.
func TestFoldStoppedVMs(t *testing.T) {
	vmo := func(ns, name string) kubevirtcorev1.VirtualMachine {
		v := kubevirtcorev1.VirtualMachine{}
		v.Namespace, v.Name = ns, name
		return v
	}
	cs := model.ClusterSummary{VMs: map[string]int{"running": 2}}
	live := map[string]clusterstate.LiveVM{
		"demo/casd":         {Phase: "Running"},
		"demo/test":         {Phase: "Running"},
		"demo/recovery-web": {}, // VM object exists, no instance
	}
	foldStoppedVMs(&cs, []kubevirtcorev1.VirtualMachine{
		vmo("demo", "casd"), vmo("demo", "test"), vmo("demo", "recovery-web"),
	}, live)
	if cs.VMs["running"] != 2 || cs.VMs["stopped"] != 1 {
		t.Fatalf("vms = %v, want running:2 stopped:1", cs.VMs)
	}

	// All running: the map is left untouched (no zero entry).
	cs = model.ClusterSummary{VMs: map[string]int{"running": 1}}
	foldStoppedVMs(&cs, []kubevirtcorev1.VirtualMachine{vmo("demo", "casd")}, live)
	if _, ok := cs.VMs["stopped"]; ok {
		t.Fatal("no stopped VMs must add no entry")
	}

	// Nil map (metrics returned nothing): still reports the stopped VM.
	cs = model.ClusterSummary{}
	foldStoppedVMs(&cs, []kubevirtcorev1.VirtualMachine{vmo("demo", "recovery-web")}, live)
	if cs.VMs["stopped"] != 1 {
		t.Fatalf("vms = %v, want stopped:1", cs.VMs)
	}
}

// A network's attachments are the VM interfaces that resolve to it: a pod
// interface rides the namespace's primary network when one exists, a multus
// reference resolves a project network by "namespace/name" (bare names are
// namespace-relative) and a shared network by name.
func TestNetworkAttachments(t *testing.T) {
	nets := []model.Network{
		{Name: "web-net", Kind: model.NetworkDefault, Scope: model.ScopeProject, Namespace: "web-prod", AttachRef: "web-prod/web-net"},
		{Name: "l2", Kind: model.NetworkInternal, Scope: model.ScopeProject, Namespace: "db-prod", AttachRef: "db-prod/l2"},
		{Name: "l2", Kind: model.NetworkInternal, Scope: model.ScopeProject, Namespace: "web-prod", AttachRef: "web-prod/l2"},
		{Name: "lan-101", Kind: model.NetworkVLAN, Scope: model.ScopeShared, AttachRef: "lan-101", Namespaces: []string{"web-prod", "db-prod"}},
	}
	vm := func(ns, name string, nws ...kubevirtcorev1.Network) kubevirtcorev1.VirtualMachine {
		return kubevirtcorev1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: kubevirtcorev1.VirtualMachineSpec{Template: &kubevirtcorev1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtcorev1.VirtualMachineInstanceSpec{Networks: nws},
			}},
		}
	}
	pod := func(name string) kubevirtcorev1.Network {
		return kubevirtcorev1.Network{Name: name, NetworkSource: kubevirtcorev1.NetworkSource{Pod: &kubevirtcorev1.PodNetwork{}}}
	}
	multus := func(name, ref string) kubevirtcorev1.Network {
		return kubevirtcorev1.Network{Name: name, NetworkSource: kubevirtcorev1.NetworkSource{Multus: &kubevirtcorev1.MultusNetwork{NetworkName: ref}}}
	}
	vms := []kubevirtcorev1.VirtualMachine{
		vm("web-prod", "web-1", pod("default"), multus("lan", "lan-101"), multus("l2", "l2")),
		vm("db-prod", "db-1", pod("default"), multus("lan", "db-prod/lan-101"), multus("l2", "db-prod/l2")),
	}
	str := func(as []metrics.Attachment) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.Namespace+"/"+a.Name+"/"+a.Interface)
		}
		return out
	}
	for _, c := range []struct {
		target model.Network
		want   []string
	}{
		{nets[0], []string{"web-prod/web-1/default"}},
		{model.Network{}, []string{"db-prod/db-1/default"}},
		{nets[1], []string{"db-prod/db-1/l2"}},
		{nets[2], []string{"web-prod/web-1/l2"}},
		{nets[3], []string{"web-prod/web-1/lan", "db-prod/db-1/lan"}},
	} {
		got := str(networkAttachments(vms, nets, c.target))
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s/%s: attachments = %v, want %v", c.target.Namespace, c.target.Name, got, c.want)
		}
	}
}
