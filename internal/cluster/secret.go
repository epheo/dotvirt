package cluster

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

// SecretValue reads one key of a Secret under THIS client's identity: the
// caller's RBAC is the gate, and dotvirt's SA never reads it.
func (c *Client) SecretValue(ctx context.Context, namespace, name, key string) (string, error) {
	sec, err := c.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	v, ok := sec.Data[key]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("%w: secret %s/%s holds no %q", model.ErrNotFound, namespace, name, key)
	}
	return string(v), nil
}
