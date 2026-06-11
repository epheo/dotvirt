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
