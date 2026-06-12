package cluster

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

// ListQuotas returns every ResourceQuota in the given namespaces under the
// caller's token — the project capacity band's read. A namespace whose quotas
// the token can't read contributes nothing (quota reads may be denied where VM
// reads are allowed); usage/caps are pre-parsed to floats for the UI's bars.
func (c *Client) ListQuotas(ctx context.Context, namespaces []string) ([]model.NamespaceQuota, error) {
	out := []model.NamespaceQuota{}
	for _, ns := range namespaces {
		list, err := c.kube.CoreV1().ResourceQuotas(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for i := range list.Items {
			out = append(out, quotaFrom(&list.Items[i]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// quotaFrom flattens one quota's status into sorted usage rows. Status.Hard is
// the enforced cap (Spec.Hard not yet reconciled is invisible to usage anyway).
func quotaFrom(q *corev1.ResourceQuota) model.NamespaceQuota {
	nq := model.NamespaceQuota{Namespace: q.Namespace, Name: q.Name}
	names := make([]string, 0, len(q.Status.Hard))
	for r := range q.Status.Hard {
		names = append(names, string(r))
	}
	sort.Strings(names)
	for _, r := range names {
		hard := q.Status.Hard[corev1.ResourceName(r)]
		used := q.Status.Used[corev1.ResourceName(r)]
		nq.Items = append(nq.Items, model.QuotaItem{
			Resource: r,
			Used:     used.AsApproximateFloat64(),
			Hard:     hard.AsApproximateFloat64(),
			Unit:     quotaUnit(r),
		})
	}
	return nq
}

// quotaUnit derives a display unit from the quota's resource name.
func quotaUnit(resource string) string {
	switch {
	case strings.Contains(resource, "cpu"):
		return "cores"
	case strings.Contains(resource, "memory"), strings.Contains(resource, "storage"):
		return "bytes"
	default:
		return "count"
	}
}
