package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DryRun is how the apiserver answered a dry-run request.
type DryRun int

const (
	// DryRunAllowed: the request would have succeeded.
	DryRunAllowed DryRun = iota
	// DryRunExists: a create found the object there. The apiserver reports it
	// after authorizing the create (and, for a binding, weighing its
	// escalation), but the real effect is an update.
	DryRunExists
	// DryRunDenied: the caller's RBAC or a binding's escalation rule refused it.
	DryRunDenied
	// DryRunNoNamespace: the namespace does not exist yet. Authorization
	// passed; nothing past namespace admission was weighed.
	DryRunNoNamespace
	// DryRunRefused: authorization passed and something later refused the
	// object: validation, or an admission webhook. Only webhooks declaring
	// sideEffects None or NoneOnDryRun run on a dry run; any other refuses it.
	DryRunRefused
)

// DryRun sends raw to the apiserver, byte for byte, as this token's create
// (verb "create", a POST) or replace (verb "update", a PUT of name) of an
// object of group/version/resource in namespace ("" for cluster scope), with
// dryRun=All so nothing persists. The bytes are not re-encoded: what the
// apiserver weighs is what Argo would send it, read by its own
// case-sensitive decoder. refused is the apiserver's reason for
// DryRunRefused. A failure to get an answer (an outage, a rejected token,
// throttling) is the error.
func (c *Client) DryRun(ctx context.Context, verb, group, version, resource, namespace, name string, raw []byte) (out DryRun, refused, err error) {
	path := []string{"/apis", group, version}
	if group == "" {
		path = []string{"/api", version}
	}
	if namespace != "" {
		path = append(path, "namespaces", namespace)
	}
	path = append(path, resource)
	rc := c.kube.CoreV1().RESTClient()
	req := rc.Post()
	switch verb {
	case "create":
	case "update":
		req = rc.Put()
		path = append(path, name)
	default:
		return 0, nil, fmt.Errorf("dry run of a %s", verb)
	}
	err = req.AbsPath(path...).Param("dryRun", metav1.DryRunAll).
		SetHeader("Content-Type", "application/json").Body(raw).Do(ctx).Error()
	switch {
	case err == nil:
		return DryRunAllowed, nil, nil
	case apierrors.IsAlreadyExists(err):
		return DryRunExists, nil, nil
	case apierrors.IsForbidden(err):
		return DryRunDenied, nil, nil
	case apierrors.IsNotFound(err):
		return DryRunNoNamespace, nil, nil
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		if code := status.Status().Code; code >= 400 && code < 500 && code != http.StatusUnauthorized && code != http.StatusTooManyRequests {
			return DryRunRefused, err, nil
		}
	}
	return 0, nil, fmt.Errorf("dry run of %s %s/%s: %w", resource, namespace, name, err)
}
