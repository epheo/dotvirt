package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/model"
)

// A claim's name is free once it is deleted, and a new claim under it may be
// another person's. Each write below is held to the claim the caller saw, by
// its UID, so it never lands on a later one.

// SetClaimRunning starts or stops the desktop a DeskuBe claim holds: its
// spec.running, which the pool carries to its desktop. Called on the caller's
// client.
func (c *Client) SetClaimRunning(ctx context.Context, namespace, claim, uid string, running bool) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": uid},
		{"op": "add", "path": "/spec/running", "value": running},
	})
	if err != nil {
		return err
	}
	_, err = dyn.Resource(desktops.GVRClaim).Namespace(namespace).Patch(ctx, claim, types.JSONPatchType, patch, metav1.PatchOptions{})
	// The apiserver answers a failed test with a 422.
	return another(err, claim, apierrors.IsInvalid(err))
}

// DeleteClaim deletes a DeskuBe claim, on the caller's client. Its desktop and
// home go with it.
func (c *Client) DeleteClaim(ctx context.Context, namespace, claim, uid string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	err = dyn.Resource(desktops.GVRClaim).Namespace(namespace).Delete(ctx, claim, metav1.DeleteOptions{Preconditions: metav1.NewUIDPreconditions(uid)})
	return another(err, claim, apierrors.IsConflict(err))
}

// another is err, said as a conflict when failed is set: the claim under that
// name is not the one the caller saw.
func another(err error, claim string, failed bool) error {
	if failed {
		return fmt.Errorf("%w: claim %s is not the one you saw: it was deleted, and its name may be another person's now; look at it again", model.ErrConflict, claim)
	}
	return err
}
