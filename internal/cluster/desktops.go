package cluster

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/epheo/dotvirt/internal/desktops"
)

// SetClaimRunning starts or stops the desktop a DeskuBe claim holds: a merge
// patch of the claim's spec.running, which the pool carries to its desktop.
// Called on the caller's client.
func (c *Client) SetClaimRunning(ctx context.Context, namespace, claim string, running bool) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	patch := fmt.Appendf(nil, `{"spec":{"running":%t}}`, running)
	_, err = dyn.Resource(desktops.GVRClaim).Namespace(namespace).Patch(ctx, claim, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// DeleteClaim deletes a DeskuBe claim, on the caller's client. Its desktop and
// home go with it.
func (c *Client) DeleteClaim(ctx context.Context, namespace, claim string) error {
	dyn, err := c.dynamic()
	if err != nil {
		return err
	}
	return dyn.Resource(desktops.GVRClaim).Namespace(namespace).Delete(ctx, claim, metav1.DeleteOptions{})
}

// RestartDesktop deletes the pods running desktop name, on the caller's
// client, and reports how many it found. Its Deployment makes a new one; the
// operator owns that Deployment, so it is never touched. Each delete is held
// to the pod listed, so one made since is left alone.
func (c *Client) RestartDesktop(ctx context.Context, namespace, name string) (int, error) {
	sel, err := labels.ValidatedSelectorFromSet(labels.Set{desktops.InstanceLabel: name})
	if err != nil {
		return 0, err
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return 0, err
	}
	for _, p := range pods.Items {
		err := c.kube.CoreV1().Pods(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{Preconditions: metav1.NewUIDPreconditions(string(p.UID))})
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, err
		}
	}
	return len(pods.Items), nil
}
