package argo

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/epheo/dotvirt/internal/model"
)

// Resync triggers an ArgoCD sync of the Application managing the given VM, so the
// cluster is reconciled back to git (main→running drift reconcile). It finds the
// managing Application by scanning status.resources[] for the VM, then requests a
// sync by setting the Application's operation field — using the k8s API dotvirt
// already has, no separate Argo API token.
func (c *Client) Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error) {
	apps, err := c.dyn.Resource(applicationsGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return model.ResyncResult{}, fmt.Errorf("list applications: %w", err)
	}

	app, found := findManagingApp(apps.Items, namespace, name)
	if !found {
		return model.ResyncResult{}, fmt.Errorf("no ArgoCD Application manages %s/%s", namespace, name)
	}

	revision, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if revision == "" {
		revision = "HEAD"
	}

	// Request a sync via the operation field — Argo's controller picks this up.
	patch := fmt.Sprintf(
		`{"operation":{"initiatedBy":{"username":"dotvirt"},"sync":{"revision":%q},"info":[{"name":"reason","value":"dotvirt re-sync from git"}]}}`,
		revision,
	)
	_, err = c.dyn.Resource(applicationsGVR).Namespace(app.GetNamespace()).Patch(
		ctx, app.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return model.ResyncResult{}, fmt.Errorf("trigger sync of %s: %w", app.GetName(), err)
	}
	return model.ResyncResult{Application: app.GetName(), Revision: revision}, nil
}

// findManagingApp returns the Application whose status.resources[] includes the
// VirtualMachine (namespace, name).
func findManagingApp(apps []unstructured.Unstructured, namespace, name string) (*unstructured.Unstructured, bool) {
	for i := range apps {
		resources, found, err := unstructured.NestedSlice(apps[i].Object, "status", "resources")
		if err != nil || !found {
			continue
		}
		for _, raw := range resources {
			res, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if asString(res, "kind") == "VirtualMachine" &&
				asString(res, "namespace") == namespace &&
				asString(res, "name") == name {
				return &apps[i], true
			}
		}
	}
	return nil, false
}
