package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

// ErrNoNamespace: a dry run named a namespace that does not exist yet, so
// the apiserver answered before weighing the object at all.
var ErrNoNamespace = errors.New("the namespace does not exist yet")

// DryRunRoleBinding asks the apiserver to create (verb "create") or replace
// (verb "update") the RoleBinding raw holds under this token, persisting
// nothing: the verb's RBAC and the escalation rule a binding faces (bind on
// the role, or holding every permission it grants) answer as they would for
// the real request. existed is a create finding the binding already there,
// which the apiserver reports only after both checks passed; the real effect
// is then an update, to be asked as one.
func (c *Client) DryRunRoleBinding(ctx context.Context, verb string, raw []byte) (allowed, existed bool, err error) {
	var rb rbacv1.RoleBinding
	if err := json.Unmarshal(raw, &rb); err != nil {
		return false, false, fmt.Errorf("%w: RoleBinding: %v", model.ErrInvalid, err)
	}
	bindings := c.kube.RbacV1().RoleBindings(rb.Namespace)
	dry := []string{metav1.DryRunAll}
	switch verb {
	case "create":
		_, err = bindings.Create(ctx, &rb, metav1.CreateOptions{DryRun: dry})
	case "update":
		_, err = bindings.Update(ctx, &rb, metav1.UpdateOptions{DryRun: dry})
	default:
		return false, false, fmt.Errorf("dry run of a RoleBinding %s", verb)
	}
	switch {
	case err == nil:
		return true, false, nil
	case apierrors.IsAlreadyExists(err):
		return true, true, nil
	case apierrors.IsForbidden(err):
		return false, false, nil
	case apierrors.IsNotFound(err):
		return false, false, ErrNoNamespace
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return false, false, fmt.Errorf("%w: RoleBinding %s/%s: %v", model.ErrInvalid, rb.Namespace, rb.Name, err)
	}
	return false, false, fmt.Errorf("dry run of RoleBinding %s/%s: %w", rb.Namespace, rb.Name, err)
}
