package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			Replication:   classReplication(sc, provisioner),
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
		Topology: strings.Join(vals, ","),
		Free:     quantityBytes(u.Object, "capacity"),
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

// ListClaims returns the PersistentVolumeClaims in namespaces under this
// client's token: the live side of VM disks, chiefly the host a local disk is
// bound to.
func (c *Client) ListClaims(ctx context.Context, namespaces []string) ([]model.Claim, error) {
	out := []model.Claim{}
	err := eachNamespace(ctx, namespaces, func(ctx context.Context, ns string) (func(), error) {
		list, err := c.kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list claims in %s: %w", ns, err)
		}
		return func() {
			for i := range list.Items {
				pvc := &list.Items[i]
				cl := model.Claim{
					Namespace: pvc.Namespace,
					Name:      pvc.Name,
					Node:      pvc.Annotations[selectedNodeAnnotation],
					Phase:     string(pvc.Status.Phase),
				}
				if pvc.Spec.StorageClassName != nil {
					cl.StorageClass = *pvc.Spec.StorageClassName
				}
				if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
					cl.Capacity = q.String()
				}
				out = append(out, cl)
			}
		}, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Namespace+"/"+out[a].Name < out[b].Namespace+"/"+out[b].Name
	})
	return out, nil
}

// selectedNodeAnnotation is where the scheduler records the host it bound a
// WaitForFirstConsumer claim to. A tenant can read it, unlike the PV's node
// affinity, which is cluster-scoped.
const selectedNodeAnnotation = "volume.kubernetes.io/selected-node"

// listParallel bounds the per-namespace lists a cross-namespace read issues at
// once: an admin sees every project namespace, and one at a time is slow while
// all at once trips the client's rate limiter.
const listParallel = 8

// eachNamespace runs list once per namespace, listParallel at a time, and
// applies each returned merge under one lock so callers append without their
// own. The first failure cancels the rest and is returned.
func eachNamespace(ctx context.Context, namespaces []string, list func(context.Context, string) (func(), error)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	slots := make(chan struct{}, listParallel)
	for _, ns := range namespaces {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			merge, err := list(ctx, ns)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			if firstErr == nil {
				merge()
			}
		}()
	}
	wg.Wait()
	return firstErr
}
