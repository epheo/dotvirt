package cluster

import (
	"context"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/epheo/dotvirt/internal/model"
)

var (
	gvrStorageProfiles = schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "storageprofiles"}
	gvrSnapshotClasses = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}
	gvrCSICapacities   = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "csistoragecapacities"}
)

// ListStorageClasses gathers the Storage section's fact sheet. Only the
// StorageClass list is required; the profile, snapshot-class and capacity
// sources are optional layers, so a cluster without CDI, without the snapshot
// CRDs, or with a driver that publishes no capacity still lists its classes.
func (c *Client) ListStorageClasses(ctx context.Context) ([]model.StorageClassInfo, error) {
	dyn, err := c.dynamic()
	if err != nil {
		return nil, err
	}
	classes, err := listAll(ctx, dyn, gvrStorageClasses)
	if err != nil {
		return nil, err
	}

	profiles := map[string]*unstructured.Unstructured{}
	if items, err := listAll(ctx, dyn, gvrStorageProfiles); err == nil {
		for i := range items {
			profiles[items[i].GetName()] = &items[i]
		}
	}
	// provisioner -> snapshot classes, sorted so the fallback pick is stable.
	snapClasses := map[string][]string{}
	if items, err := listAll(ctx, dyn, gvrSnapshotClasses); err == nil {
		for i := range items {
			driver, _, _ := unstructured.NestedString(items[i].Object, "driver")
			snapClasses[driver] = append(snapClasses[driver], items[i].GetName())
		}
		for _, names := range snapClasses {
			sort.Strings(names)
		}
	}
	segments := map[string][]model.CapacitySegment{}
	if items, err := listAllNS(ctx, dyn, gvrCSICapacities); err == nil {
		for i := range items {
			class, _, _ := unstructured.NestedString(items[i].Object, "storageClassName")
			segments[class] = append(segments[class], capacitySegment(&items[i]))
		}
	}

	out := make([]model.StorageClassInfo, 0, len(classes))
	for i := range classes {
		sc := &classes[i]
		name := sc.GetName()
		provisioner, _, _ := unstructured.NestedString(sc.Object, "provisioner")
		reclaim, _, _ := unstructured.NestedString(sc.Object, "reclaimPolicy")
		binding, _, _ := unstructured.NestedString(sc.Object, "volumeBindingMode")
		expandable, _, _ := unstructured.NestedBool(sc.Object, "allowVolumeExpansion")
		info := model.StorageClassInfo{
			Name:          name,
			Default:       sc.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] == "true",
			Description:   sc.GetAnnotations()["description"],
			Provisioner:   provisioner,
			ReclaimPolicy: reclaim,
			BindingMode:   binding,
			Expandable:    expandable,
		}
		if ts := sc.GetCreationTimestamp(); !ts.IsZero() {
			info.Created = ts.UTC().Format(time.RFC3339)
		}
		if p := profiles[name]; p != nil {
			info.Profile = storageProfile(p)
			info.SnapshotClass, _, _ = unstructured.NestedString(p.Object, "status", "snapshotClass")
		}
		if info.SnapshotClass == "" && len(snapClasses[provisioner]) > 0 {
			info.SnapshotClass = snapClasses[provisioner][0]
		}
		if segs := segments[name]; len(segs) > 0 {
			sort.Slice(segs, func(a, b int) bool { return segs[a].Topology < segs[b].Topology })
			var free int64
			for _, s := range segs {
				free += s.Free
			}
			info.Free = &free
			info.Segments = segs
		}
		out = append(out, info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// storageProfile reads CDI's resolved claim settings. The first property set
// is the one CDI applies when a DataVolume names none; any set offering
// ReadWriteMany makes disks on the class live-migratable.
func storageProfile(p *unstructured.Unstructured) *model.StorageProfile {
	sp := &model.StorageProfile{AccessModes: []string{}}
	sp.CloneStrategy, _, _ = unstructured.NestedString(p.Object, "status", "cloneStrategy")
	sets, _, _ := unstructured.NestedSlice(p.Object, "status", "claimPropertySets")
	for i, raw := range sets {
		set, _ := raw.(map[string]any)
		modes, _, _ := unstructured.NestedStringSlice(set, "accessModes")
		mode, _, _ := unstructured.NestedString(set, "volumeMode")
		if i == 0 {
			sp.AccessModes = append(sp.AccessModes, modes...)
			sp.VolumeMode = mode
		}
		for _, m := range modes {
			if m == "ReadWriteMany" {
				sp.Shared = true
			}
		}
	}
	conds, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
	for _, raw := range conds {
		cond, _ := raw.(map[string]any)
		if cond["type"] == "Recognized" && cond["status"] == "True" {
			sp.Recognized = true
		}
	}
	return sp
}

// capacitySegment reads one CSIStorageCapacity. The segment's identity is its
// topology selector's label values (the node name for local storage), joined
// when a driver keys on several labels.
func capacitySegment(u *unstructured.Unstructured) model.CapacitySegment {
	labels, _, _ := unstructured.NestedStringMap(u.Object, "nodeTopology", "matchLabels")
	vals := make([]string, 0, len(labels))
	for _, v := range labels {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	return model.CapacitySegment{
		Topology:  strings.Join(vals, ","),
		Free:      quantityBytes(u.Object, "capacity"),
		MaxVolume: quantityBytes(u.Object, "maximumVolumeSize"),
	}
}

func quantityBytes(obj map[string]any, field string) int64 {
	s, _, _ := unstructured.NestedString(obj, field)
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}
