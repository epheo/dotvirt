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
// caller's token - the project capacity band's read. A namespace whose quotas
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

// storageClassQuota is the suffix of the ResourceQuota resources that limit
// one storage class: <class>.storageclass.storage.k8s.io/<resource>.
const storageClassQuota = ".storageclass.storage.k8s.io/"

// ForbiddenStorageClasses names the storage classes namespace's
// ResourceQuotas allow no claim on, read under THIS client's identity.
func (c *Client) ForbiddenStorageClasses(ctx context.Context, namespace string) (map[string]bool, error) {
	list, err := c.kube.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return forbiddenClasses(list.Items), nil
}

// forbiddenClasses are the classes a hard limit of zero on their storage
// requests or their claim count shuts out. A scoped quota never does: the
// claim evaluator matches no scope but a VolumeAttributesClass one, which
// limits only claims naming that class. The limit read is Status.Hard, what
// admission enforces; Spec.Hard while the controller has not filled status,
// when admission already refuses claims the quota covers.
func forbiddenClasses(quotas []corev1.ResourceQuota) map[string]bool {
	out := map[string]bool{}
	for _, q := range quotas {
		if len(q.Spec.Scopes) > 0 || q.Spec.ScopeSelector != nil {
			continue
		}
		hard := q.Status.Hard
		if len(hard) == 0 {
			hard = q.Spec.Hard
		}
		for name, qty := range hard {
			class, res, ok := strings.Cut(string(name), storageClassQuota)
			if ok && class != "" && (res == "requests.storage" || res == "persistentvolumeclaims") && qty.IsZero() {
				out[class] = true
			}
		}
	}
	return out
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
