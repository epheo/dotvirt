package cluster

import (
	"context"
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var gvrReplicatedVolume = schema.GroupVersionResource{Group: "r-drbd.io", Version: "v1alpha1", Resource: "volumes"}

// ResolveSplitBrain names the copy of volume that wins: r-drbd discards every
// other copy's writes since the split. Called on the caller's client.
func (c *Client) ResolveSplitBrain(ctx context.Context, volume, survivor string) error {
	return c.patchReplicatedVolume(ctx, volume, map[string]any{"resolve": map[string]any{"survivor": survivor}})
}

// EvictReplica sets the hosts whose replica of volume must move. A merge patch
// replaces the list, so evict is the whole list. Called on the caller's client.
func (c *Client) EvictReplica(ctx context.Context, volume string, evict []string) error {
	return c.patchReplicatedVolume(ctx, volume, map[string]any{"evict": evict})
}

func (c *Client) patchReplicatedVolume(ctx context.Context, volume string, spec map[string]any) error {
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(gvrReplicatedVolume).Patch(ctx, volume, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
