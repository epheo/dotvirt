package cluster

import (
	"context"
	"reflect"
	"sort"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestVisibleNamespacesClusterWide(t *testing.T) {
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}},
	)
	allowSSAR(kube, true)
	c := &Client{kube: kube}

	got, err := c.VisibleNamespaces(context.Background(), nil)
	if err != nil {
		t.Fatalf("VisibleNamespaces: %v", err)
	}
	sort.Strings(got)
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func allowSSAR(kube *fake.Clientset, allowed bool) {
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		ssar.Status.Allowed = allowed
		return true, ssar, nil
	})
}

// TestVisibleNamespacesListWithoutVMRead pins the auditor-role gap: listing
// namespaces cluster-wide must not stand in for reading VMs in them — without
// the cluster-wide VM read the candidates are probed like any other token.
func TestVisibleNamespacesListWithoutVMRead(t *testing.T) {
	kube := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-b"}},
	)
	allowSSAR(kube, false)
	kube.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssrr := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectRulesReview)
		out := ssrr.DeepCopy()
		if ssrr.Spec.Namespace == "tenant-a" {
			out.Status.ResourceRules = []authzv1.ResourceRule{{
				Verbs:     []string{"get", "list"},
				APIGroups: []string{"kubevirt.io"},
				Resources: []string{"virtualmachines"},
			}}
		}
		return true, out, nil
	})

	c := &Client{kube: kube}
	got, err := c.VisibleNamespaces(context.Background(), []string{"tenant-a", "tenant-b"})
	if err != nil {
		t.Fatalf("VisibleNamespaces: %v", err)
	}
	if want := []string{"tenant-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (namespace list alone must not grant visibility)", got, want)
	}
}

// TestVisibleNamespacesForbiddenFallback drives the SSRR path: a token that may
// not list namespaces cluster-wide is probed per candidate, keeping only the ones
// where it can read VMs.
func TestVisibleNamespacesForbiddenFallback(t *testing.T) {
	kube := fake.NewSimpleClientset()

	// Deny the cluster-wide namespace list.
	kube.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", nil)
	})

	// Grant VM read only in tenant-a; tenant-b returns no useful rules.
	kube.PrependReactor("create", "selfsubjectrulesreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssrr := action.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectRulesReview)
		out := ssrr.DeepCopy()
		if ssrr.Spec.Namespace == "tenant-a" {
			out.Status.ResourceRules = []authzv1.ResourceRule{{
				Verbs:     []string{"get", "list"},
				APIGroups: []string{"kubevirt.io"},
				Resources: []string{"virtualmachines"},
			}}
		} else {
			out.Status.ResourceRules = []authzv1.ResourceRule{{
				Verbs:     []string{"get"},
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
			}}
		}
		return true, out, nil
	})

	c := &Client{kube: kube}
	got, err := c.VisibleNamespaces(context.Background(), []string{"tenant-a", "tenant-b"})
	if err != nil {
		t.Fatalf("VisibleNamespaces: %v", err)
	}
	if want := []string{"tenant-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (only namespaces granting VM read should survive)", got, want)
	}
}

// TestGrantsReadWildcards covers the "*" matching in resource rules.
func TestGrantsReadWildcards(t *testing.T) {
	star := authzv1.ResourceRule{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}}
	if !grantsRead(star, "kubevirt.io", "virtualmachines") {
		t.Error("wildcard rule should grant read")
	}
	getList := authzv1.ResourceRule{Verbs: []string{"get", "list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}
	if !grantsRead(getList, "kubevirt.io", "virtualmachines") {
		t.Error("explicit get/list rule should grant read")
	}
	watchOnly := authzv1.ResourceRule{Verbs: []string{"watch"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}
	if grantsRead(watchOnly, "kubevirt.io", "virtualmachines") {
		t.Error("watch-only rule should NOT grant read")
	}
}

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
