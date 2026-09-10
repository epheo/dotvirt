package cluster

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func storageListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		gvrStorageClasses:  "StorageClassList",
		gvrStorageProfiles: "StorageProfileList",
		gvrSnapshotClasses: "VolumeSnapshotClassList",
		gvrCSICapacities:   "CSIStorageCapacityList",
	}
}

func obj(apiVersion, kind, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name},
	}}
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

// A class joins its CDI profile, its snapshot class and the CSI driver's
// per-segment capacity; a class lacking each layer still lists, with the
// layer absent (nil Free, no profile) rather than zeroed.
func TestListStorageClassesJoinsLayers(t *testing.T) {
	fast := obj("storage.k8s.io/v1", "StorageClass", "fast", map[string]any{
		"provisioner": "topolvm.io", "reclaimPolicy": "Delete",
		"volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": true,
	})
	fast.SetAnnotations(map[string]string{
		"storageclass.kubernetes.io/is-default-class": "true",
		"description": "NVMe pool",
	})
	nfs := obj("storage.k8s.io/v1", "StorageClass", "nfs", map[string]any{"provisioner": "nfs.csi.k8s.io"})
	profile := obj("cdi.kubevirt.io/v1beta1", "StorageProfile", "fast", map[string]any{
		"status": map[string]any{
			"cloneStrategy": "snapshot",
			"snapshotClass": "fast-snap",
			"claimPropertySets": []any{
				map[string]any{"accessModes": []any{"ReadWriteOnce"}, "volumeMode": "Block"},
				map[string]any{"accessModes": []any{"ReadWriteMany"}, "volumeMode": "Filesystem"},
			},
			"conditions": []any{map[string]any{"type": "Recognized", "status": "True"}},
		},
	})
	snap := obj("snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "nfs-snap", map[string]any{"driver": "nfs.csi.k8s.io"})
	capA := obj("storage.k8s.io/v1", "CSIStorageCapacity", "csisc-a", map[string]any{
		"storageClassName": "fast", "capacity": "10Gi", "maximumVolumeSize": "4Gi",
		"nodeTopology": map[string]any{"matchLabels": map[string]any{"topology.topolvm.io/node": "node-b"}},
	})
	capA.SetNamespace("lvm")
	capB := obj("storage.k8s.io/v1", "CSIStorageCapacity", "csisc-b", map[string]any{
		"storageClassName": "fast", "capacity": "6Gi",
		"nodeTopology": map[string]any{"matchLabels": map[string]any{"topology.topolvm.io/node": "node-a"}},
	})
	capB.SetNamespace("lvm")

	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), storageListKinds(),
		fast, nfs, profile, snap, capA, capB)
	got, err := NewClient(nil, nil, dc).ListStorageClasses(context.Background())
	if err != nil {
		t.Fatalf("ListStorageClasses: %v", err)
	}
	if len(got) != 2 || got[0].Name != "fast" || got[1].Name != "nfs" {
		t.Fatalf("want [fast nfs] sorted, got %+v", got)
	}

	f := got[0]
	if !f.Default || f.Description != "NVMe pool" || f.Provisioner != "topolvm.io" ||
		f.ReclaimPolicy != "Delete" || f.BindingMode != "WaitForFirstConsumer" || !f.Expandable {
		t.Errorf("class facts = %+v", f)
	}
	if f.Profile == nil || f.Profile.CloneStrategy != "snapshot" || !f.Profile.Recognized ||
		f.Profile.VolumeMode != "Block" || len(f.Profile.AccessModes) != 1 || f.Profile.AccessModes[0] != "ReadWriteOnce" {
		t.Errorf("profile = %+v, want the first property set as the default", f.Profile)
	}
	if !f.Profile.Shared {
		t.Error("a ReadWriteMany property set anywhere in the profile must mark the class shared")
	}
	if f.SnapshotClass != "fast-snap" {
		t.Errorf("snapshotClass = %q, want CDI's pick", f.SnapshotClass)
	}
	if f.Free == nil || *f.Free != 16<<30 {
		t.Errorf("free = %v, want the segments summed (16Gi)", f.Free)
	}
	if len(f.Segments) != 2 || f.Segments[0].Topology != "node-a" || f.Segments[1].MaxVolume != 4<<30 {
		t.Errorf("segments = %+v, want sorted by topology with maxVolume kept", f.Segments)
	}

	n := got[1]
	if n.Profile != nil || n.Free != nil || n.Segments != nil {
		t.Errorf("a class without profile/capacity must carry neither: %+v", n)
	}
	if n.SnapshotClass != "nfs-snap" {
		t.Errorf("snapshotClass = %q, want the provisioner's registered class as fallback", n.SnapshotClass)
	}
}

// Missing optional CRDs (no CDI, no snapshot API) are not errors: the class
// list alone still serves.
func TestListStorageClassesWithoutOptionalKinds(t *testing.T) {
	sc := obj("storage.k8s.io/v1", "StorageClass", "plain", map[string]any{"provisioner": "x"})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), storageListKinds(), sc)
	for _, res := range []string{"storageprofiles", "volumesnapshotclasses", "csistoragecapacities"} {
		dc.PrependReactor("list", res, func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(action.GetResource().GroupResource(), "")
		})
	}
	got, err := NewClient(nil, nil, dc).ListStorageClasses(context.Background())
	if err != nil {
		t.Fatalf("ListStorageClasses: %v", err)
	}
	if len(got) != 1 || got[0].Name != "plain" || got[0].Profile != nil || got[0].Free != nil {
		t.Errorf("got %+v", got)
	}
}
