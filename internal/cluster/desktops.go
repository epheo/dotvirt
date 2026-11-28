package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/model"
)

// A claim's or a desktop's name is free once it is deleted, and a new one
// under it may be another person's. Each write below is held to the object
// the caller saw, by its UID, so it never lands on a later one. A patch
// carries the UID in metadata: the apiserver refuses another as an immutable
// field, naming metadata.uid. A JSON patch's failed test would not do: the
// apiserver answers it with a bare 422, alike to any invalid field's.

// SetClaimRunning starts or stops the desktop a DeskuBe claim holds: its
// spec.running, which the pool carries to its desktop. Called on the caller's
// client.
func (c *Client) SetClaimRunning(ctx context.Context, namespace, claim, uid string, running bool) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": uid},
		"spec":     map[string]any{"running": running},
	})
	if err != nil {
		return err
	}
	_, err = dyn.Resource(desktops.GVRClaim).Namespace(namespace).Patch(ctx, claim, types.MergePatchType, patch, metav1.PatchOptions{})
	return patchRefusal(err, anotherClaim(claim))
}

// DeleteClaim deletes a DeskuBe claim, on the caller's client. Its desktop and
// home go with it.
func (c *Client) DeleteClaim(ctx context.Context, namespace, claim, uid string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	err = dyn.Resource(desktops.GVRClaim).Namespace(namespace).Delete(ctx, claim, metav1.DeleteOptions{Preconditions: metav1.NewUIDPreconditions(uid)})
	if apierrors.IsConflict(err) {
		return anotherClaim(claim)
	}
	return err
}

// RestartDesktop asks DeskuBe to restart a desktop: a new value of its restart
// annotation, which DeskuBe rolls out to the desktop's pod, ending the session
// of whoever owns it. A pool gives a warm desktop to a claim in place, under
// the same UID, so the UID alone does not hold the write to the person the
// caller saw: the desktop is read first, its UID and owner checked, and the
// patch sent at that read's resourceVersion. Called on the caller's client.
func (c *Client) RestartDesktop(ctx context.Context, namespace, name, uid, owner string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	res := dyn.Resource(desktops.GVRInstance).Namespace(namespace)
	live, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	madeAgain := fmt.Errorf("%w: desktop %s is not the one you saw: it was deleted, and another made under its name; look at it again", model.ErrConflict, name)
	if string(live.GetUID()) != uid {
		return madeAgain
	}
	if now, _, _ := unstructured.NestedString(live.Object, "spec", "owner"); now != owner {
		return ChangedHands(name)
	}
	// The same value again restarts nothing, and DeskuBe rolls out none over
	// 64 bytes: the time to the nanosecond is new per request, and 30 at most.
	asked := time.Now().UTC().Format(time.RFC3339Nano)
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"uid":             uid,
		"resourceVersion": live.GetResourceVersion(),
		"annotations":     map[string]string{desktops.RestartAnnotation: asked},
	}})
	if err != nil {
		return err
	}
	_, err = res.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsConflict(err) {
		return fmt.Errorf("%w: desktop %s changed since you saw it: look at it again", model.ErrConflict, name)
	}
	return patchRefusal(err, madeAgain)
}

// ChangedHands says the desktop name has another owner than the caller saw.
func ChangedHands(name string) error {
	return fmt.Errorf("%w: desktop %s changed hands since you saw it: look at it again", model.ErrConflict, name)
}

// anotherClaim says the claim under that name is not the one the caller saw.
func anotherClaim(claim string) error {
	return fmt.Errorf("%w: claim %s is not the one you saw: it was deleted, and its name may be another person's now; look at it again", model.ErrConflict, claim)
}

// patchRefusal is err, a patch's answer, as the caller reads it: other when
// the apiserver refused the metadata.uid it carries, else the apiserver's own.
func patchRefusal(err, other error) error {
	var status apierrors.APIStatus
	if !apierrors.IsInvalid(err) || !errors.As(err, &status) {
		return err
	}
	if d := status.Status().Details; d != nil && slices.ContainsFunc(d.Causes, func(c metav1.StatusCause) bool {
		return c.Field == "metadata.uid"
	}) {
		return other
	}
	return err
}
