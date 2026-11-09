package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/epheo/dotvirt/internal/model"
)

var gvrReplicatedVolume = schema.GroupVersionResource{Group: "r-drbd.io", Version: "v1alpha1", Resource: "volumes"}

// ResolveSplitBrain names the copy of volume that wins, on the caller's client.
// r-drbd then discards every other copy's writes since the split, so it reads
// the Volume live and refuses unless this is still the split the operator
// decided on. The read's resourceVersion makes anything landing before the
// write a conflict, and r-drbd has no rule that would stop a second survivor.
func (c *Client) ResolveSplitBrain(ctx context.Context, volume, survivor, since string) error {
	u, err := c.dyn.Resource(gvrReplicatedVolume).Get(ctx, volume, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var doc volumeForResolve
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &doc); err != nil {
		return err
	}
	if err := doc.check(volume, survivor, since); err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": u.GetResourceVersion()},
		"spec":     map[string]any{"resolve": map[string]any{"survivor": survivor}},
	})
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(gvrReplicatedVolume).Patch(ctx, volume, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// volumeForResolve names what a resolve weighs. Not spec.assignment.secret.
type volumeForResolve struct {
	Spec struct {
		Resolve struct {
			Survivor string `json:"survivor"`
		} `json:"resolve"`
		Assignment struct {
			Replicas []struct {
				Node string `json:"node"`
				Role string `json:"role"`
			} `json:"replicas"`
		} `json:"assignment"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type               string `json:"type"`
			Status             string `json:"status"`
			LastTransitionTime string `json:"lastTransitionTime"`
		} `json:"conditions"`
	} `json:"status"`
}

func (d volumeForResolve) check(volume, survivor, since string) error {
	var current string
	for _, c := range d.Status.Conditions {
		if c.Type == "SplitBrain" && c.Status == "True" {
			current = c.LastTransitionTime
		}
	}
	if current == "" {
		return fmt.Errorf("%w: the copies of %s no longer disagree", model.ErrConflict, volume)
	}
	if current != since {
		return fmt.Errorf("%w: this is a newer split of %s, look at it again", model.ErrConflict, volume)
	}
	if named := d.Spec.Resolve.Survivor; named != "" && named != survivor {
		return fmt.Errorf("%w: %s is already being resolved in favor of %s", model.ErrConflict, volume, named)
	}
	if !slices.ContainsFunc(d.Spec.Assignment.Replicas, func(r struct {
		Node string `json:"node"`
		Role string `json:"role"`
	}) bool {
		return r.Node == survivor && r.Role == "diskful"
	}) {
		return fmt.Errorf("%w: %s holds no copy of %s", model.ErrConflict, survivor, volume)
	}
	return nil
}

// EvictReplica sets the hosts whose replica of volume must move. A merge patch
// replaces the list, so evict is the whole list. Called on the caller's client.
func (c *Client) EvictReplica(ctx context.Context, volume string, evict []string) error {
	return c.patchReplicatedVolume(ctx, volume, map[string]any{"evict": evict})
}

// patchReplicatedVolume applies a merge patch to volume's spec.
func (c *Client) patchReplicatedVolume(ctx context.Context, volume string, spec map[string]any) error {
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(gvrReplicatedVolume).Patch(ctx, volume, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
