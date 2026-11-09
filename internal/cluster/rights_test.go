package cluster

import (
	"context"
	"errors"
	"slices"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var vmsAndPolicies = []schema.GroupResource{{Group: "kubevirt.io", Resource: "virtualmachines"}, {Group: "networking.k8s.io", Resource: "networkpolicies"}}

func rulesClient(answer func(ns string) (authzv1.SubjectRulesReviewStatus, error)) *Client {
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "selfsubjectrulesreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectRulesReview).DeepCopy()
		status, err := answer(r.Spec.Namespace)
		r.Status = status
		return true, r, err
	})
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview).DeepCopy()
		r.Status.Allowed = r.Spec.ResourceAttributes.Resource == "networkpolicies"
		return true, r, nil
	})
	return NewClient(kube, nil, nil)
}

func TestListableInReadsOneReviewPerNamespace(t *testing.T) {
	c := rulesClient(func(string) (authzv1.SubjectRulesReviewStatus, error) {
		return authzv1.SubjectRulesReviewStatus{ResourceRules: []authzv1.ResourceRule{
			{Verbs: []string{"get", "list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"*"}},
			// A list names no object: a rule that does cannot answer one.
			{Verbs: []string{"list"}, APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, ResourceNames: []string{"one"}},
		}}, nil
	})
	got, err := c.ListableIn(context.Background(), []string{"a", "b"}, vmsAndPolicies)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"a", "b"} {
		if !slices.Equal(got[ns], []bool{true, false}) {
			t.Errorf("%s = %v", ns, got[ns])
		}
	}
}

// A webhook authorizer cannot enumerate rules: the apiserver says so.
func TestListableInAsksKindByKindWhenIncomplete(t *testing.T) {
	c := rulesClient(func(string) (authzv1.SubjectRulesReviewStatus, error) {
		return authzv1.SubjectRulesReviewStatus{Incomplete: true}, nil
	})
	got, err := c.ListableIn(context.Background(), []string{"a"}, vmsAndPolicies)
	if err != nil || !slices.Equal(got["a"], []bool{false, true}) {
		t.Errorf("got %v, %v", got, err)
	}
}

// A smaller answer would read as lost access, and be cached.
func TestListableInFailsWhole(t *testing.T) {
	c := rulesClient(func(ns string) (authzv1.SubjectRulesReviewStatus, error) {
		if ns == "b" {
			return authzv1.SubjectRulesReviewStatus{}, errors.New("down")
		}
		return authzv1.SubjectRulesReviewStatus{}, nil
	})
	if got, err := c.ListableIn(context.Background(), []string{"a", "b"}, vmsAndPolicies); err == nil {
		t.Errorf("got %v with no error", got)
	}
}
