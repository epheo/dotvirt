package cluster

import (
	"context"
	"fmt"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

// Permissions evaluates the caller's effective capabilities in namespace with one
// SelfSubjectRulesReview — the Permissions tab's read. The list is curated to the
// actions the UI performs under the user's own token; it checks the
// subresources.kubevirt.io group for console/restart/pause/migrate, because
// that — not kubevirt.io — is what actually gates those calls (a tab that checked
// the wrong group would contradict what the Actions menu can do). Config, power,
// and delete go through the PR flow: the forge gates them, not cluster RBAC.
func (c *Client) Permissions(ctx context.Context, namespace string) (model.Permissions, error) {
	review := &authzv1.SelfSubjectRulesReview{
		Spec: authzv1.SelfSubjectRulesReviewSpec{Namespace: namespace},
	}
	res, err := c.kube.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return model.Permissions{}, fmt.Errorf("rules review for %s: %w", namespace, err)
	}
	rules := res.Status.ResourceRules

	capability := func(id, label, group, resource, verb string) model.Capability {
		return model.Capability{
			ID:      id,
			Label:   label,
			Allowed: ruleAllows(rules, group, resource, verb),
			Detail:  fmt.Sprintf("%s %s (%s)", group, resource, verb),
		}
	}
	return model.Permissions{
		Namespace:  namespace,
		Incomplete: res.Status.Incomplete,
		Capabilities: []model.Capability{
			capability("view", "View virtual machines", "kubevirt.io", "virtualmachines", "list"),
			capability("console", "Open console (VNC)", "subresources.kubevirt.io", "virtualmachineinstances/vnc", "get"),
			capability("restart", "Restart", "subresources.kubevirt.io", "virtualmachines/restart", "update"),
			capability("pause", "Pause / unpause", "subresources.kubevirt.io", "virtualmachineinstances/pause", "update"),
			capability("migrate", "Live-migrate", "subresources.kubevirt.io", "virtualmachines/migrate", "update"),
			capability("snapshot", "Take snapshots", "snapshot.kubevirt.io", "virtualmachinesnapshots", "create"),
			capability("restore", "Restore snapshots", "snapshot.kubevirt.io", "virtualmachinerestores", "create"),
			capability("clone", "Clone", "clone.kubevirt.io", "virtualmachineclones", "create"),
			capability("resync", "Trigger Argo re-sync", "kubevirt.io", "virtualmachines", "update"),
		},
	}, nil
}

// ruleAllows reports whether any rule grants verb on group/resource. A rule
// scoped to specific resourceNames still counts: the capability exists for at
// least some objects in the namespace.
func ruleAllows(rules []authzv1.ResourceRule, group, resource, verb string) bool {
	for _, r := range rules {
		if contains(r.APIGroups, group) && contains(r.Resources, resource) && contains(r.Verbs, verb) {
			return true
		}
	}
	return false
}
