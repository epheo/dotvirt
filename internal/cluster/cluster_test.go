package cluster

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestSetNodeMaintenanceRoundTrip drives enter/exit through the fake's real
// strategic-merge patching: one patch must flip the annotation and cordon
// together, and exit must remove the annotation (null value), not blank it.
func TestSetNodeMaintenanceRoundTrip(t *testing.T) {
	kube := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
	)
	c := &Client{kube: kube}
	ctx := context.Background()

	if err := c.SetNodeMaintenance(ctx, "w1", true); err != nil {
		t.Fatalf("enter: %v", err)
	}
	n, _ := kube.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{})
	if !n.Spec.Unschedulable {
		t.Error("enter should cordon the node")
	}
	if n.Annotations[maintenanceAnnotation] == "" {
		t.Error("enter should set the maintenance annotation")
	}

	if err := c.SetNodeMaintenance(ctx, "w1", false); err != nil {
		t.Fatalf("exit: %v", err)
	}
	n, _ = kube.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{})
	if n.Spec.Unschedulable {
		t.Error("exit should uncordon the node")
	}
	if _, ok := n.Annotations[maintenanceAnnotation]; ok {
		t.Error("exit should remove the maintenance annotation")
	}
}

// TestNodeInfoMaintenanceSurvivesUncordon: the annotation is the intent marker,
// so an out-of-band uncordon must not silently end maintenance mode.
func TestNodeInfoMaintenanceSurvivesUncordon(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        "w1",
		Annotations: map[string]string{maintenanceAnnotation: "true"},
	}})
	c := &Client{kube: kube}

	info, err := c.NodeInfo(context.Background(), "w1")
	if err != nil {
		t.Fatalf("NodeInfo: %v", err)
	}
	if !info.Maintenance {
		t.Error("maintenance should report from the annotation, not cordon state")
	}
	if info.Unschedulable {
		t.Error("unschedulable should reflect the node spec")
	}
}

func TestListNodesMaintenance(t *testing.T) {
	sched := map[string]string{"kubevirt.io/schedulable": "true"}
	kube := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1", Labels: sched}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:        "w2",
			Labels:      sched,
			Annotations: map[string]string{maintenanceAnnotation: "true"},
		}},
	)
	c := &Client{kube: kube}

	nodes, err := c.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Maintenance || !nodes[1].Maintenance {
		t.Errorf("want w1 not in maintenance, w2 in maintenance; got %+v", nodes)
	}
}

// LiveNamespace keeps what the namespace's author declared and drops what the
// cluster stamps on every namespace.
func TestLiveNamespaceKeepsAuthorMetadata(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "tenant-a",
		Labels: map[string]string{
			"tenant": "a", "k8s.ovn.org/primary-user-defined-network": "",
			"pod-security.kubernetes.io/enforce":      "restricted",
			"kubernetes.io/metadata.name":             "tenant-a",
			"pod-security.kubernetes.io/audit":        "restricted",
			"pod-security.kubernetes.io/warn-version": "latest",
		},
		Annotations: map[string]string{
			"openshift.io/description":                                     "roundtable",
			"kubectl.kubernetes.io/last-applied-configuration":             "{}",
			"openshift.io/sa.scc.uid-range":                                "1000/10000",
			"security.openshift.io/MinimallySufficientPodSecurityStandard": "restricted",
		},
	}})
	c := &Client{kube: kube}
	got, err := c.LiveNamespace(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("LiveNamespace: %v", err)
	}
	wantLabels := map[string]string{
		"tenant": "a", "k8s.ovn.org/primary-user-defined-network": "", "pod-security.kubernetes.io/enforce": "restricted",
	}
	if !reflect.DeepEqual(got.Labels, wantLabels) {
		t.Errorf("labels = %v, want %v", got.Labels, wantLabels)
	}
	if want := map[string]string{"openshift.io/description": "roundtable"}; !reflect.DeepEqual(got.Annotations, want) {
		t.Errorf("annotations = %v, want %v", got.Annotations, want)
	}
	if absent, err := c.LiveNamespace(context.Background(), "new"); err != nil || absent != nil {
		t.Errorf("a namespace that does not exist must read as nil, got %v, %v", absent, err)
	}
}

// The node LIST uplinks already read carries each node's OVN-Kubernetes pod
// subnets, the default network's range the trace places addresses in.
func TestListNodeLabelsCarriesPodSubnets(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "w1", Labels: map[string]string{"zone": "a"},
		Annotations: map[string]string{"k8s.ovn.org/node-subnets": `{"default":["10.128.0.0/23"]}`},
	}})
	c := &Client{kube: kube}
	got, err := c.ListNodeLabels(context.Background())
	if err != nil || len(got) != 1 || got[0].Labels["zone"] != "a" || got[0].PodSubnets != `{"default":["10.128.0.0/23"]}` {
		t.Fatalf("ListNodeLabels = %+v, %v", got, err)
	}
}
