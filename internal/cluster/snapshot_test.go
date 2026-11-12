package cluster

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/epheo/dotvirt/internal/model"
)

func restoreClient() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrRestores: "VirtualMachineRestoreList"})
}

// A restore keeps the VM's disk names, so git and the VM still agree after it.
func TestRestoreKeepsTheDiskNames(t *testing.T) {
	dyn := restoreClient()
	if err := NewClient(nil, nil, dyn).RestoreSnapshot(context.Background(), "alpha", "web", "snap"); err != nil {
		t.Fatal(err)
	}
	var created *k8stesting.CreateActionImpl
	for _, a := range dyn.Actions() {
		if c, ok := a.(k8stesting.CreateActionImpl); ok && c.GetResource() == gvrRestores {
			created = &c
		}
	}
	if created == nil {
		t.Fatal("no VirtualMachineRestore created")
	}
	if got := created.CreateOptions.FieldValidation; got != metav1.FieldValidationStrict {
		t.Errorf("field validation = %q, want Strict", got)
	}
	policy, _, _ := unstructured.NestedString(created.Object.(*unstructured.Unstructured).Object, "spec", "volumeRestorePolicy")
	if policy != "InPlace" {
		t.Errorf("volumeRestorePolicy = %q, want InPlace", policy)
	}
}

// A KubeVirt without the policy refuses the field under strict validation:
// the restore is refused, never run under new disk names.
func TestRestoreRefusedWithoutInPlace(t *testing.T) {
	dyn := restoreClient()
	dyn.PrependReactor("create", "virtualmachinerestores", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewBadRequest(`VirtualMachineRestore in version "v1beta1" cannot be handled as a VirtualMachineRestore: strict decoding error: unknown field "spec.volumeRestorePolicy"`)
	})
	err := NewClient(nil, nil, dyn).RestoreSnapshot(context.Background(), "alpha", "web", "snap")
	if !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
