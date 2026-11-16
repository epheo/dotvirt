package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func hardList(hard map[string]string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, v := range hard {
		out[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	return out
}

// A class is shut out of a namespace by a hard limit of zero on its storage
// requests or its claim count, as admission enforces it: status's limit,
// spec's until the controller fills status. A non-zero limit, another
// class's limit, any other resource or a scoped quota leaves it offered.
func TestForbiddenClasses(t *testing.T) {
	quota := func(spec, status map[string]string) corev1.ResourceQuota {
		q := corev1.ResourceQuota{Spec: corev1.ResourceQuotaSpec{Hard: hardList(spec)}}
		if status != nil {
			q.Status.Hard = hardList(status)
		}
		return q
	}
	enforced := func(hard map[string]string) corev1.ResourceQuota { return quota(hard, hard) }
	terminating := enforced(map[string]string{"term.storageclass.storage.k8s.io/requests.storage": "0"})
	terminating.Spec.Scopes = []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeTerminating}
	byAttributes := enforced(map[string]string{"vac.storageclass.storage.k8s.io/persistentvolumeclaims": "0"})
	byAttributes.Spec.ScopeSelector = &corev1.ScopeSelector{MatchExpressions: []corev1.ScopedResourceSelectorRequirement{{
		ScopeName: corev1.ResourceQuotaScopeVolumeAttributesClass, Operator: corev1.ScopeSelectorOpIn, Values: []string{"gold"},
	}}}
	got := forbiddenClasses([]corev1.ResourceQuota{
		enforced(map[string]string{
			"lab.storageclass.storage.k8s.io/requests.storage":  "0",
			"fast.storageclass.storage.k8s.io/requests.storage": "100Gi",
			"requests.storage": "0",
			"pods":             "0",
		}),
		enforced(map[string]string{
			"replicated.storageclass.storage.k8s.io/persistentvolumeclaims": "0",
			"fast.storageclass.storage.k8s.io/persistentvolumeclaims":       "5",
			"x.storageclass.storage.k8s.io/requests.cpu":                    "0",
		}),
		// Raised in spec, not yet in status: admission still holds zero.
		quota(map[string]string{"pending.storageclass.storage.k8s.io/requests.storage": "10Gi"},
			map[string]string{"pending.storageclass.storage.k8s.io/requests.storage": "0"}),
		// Lowered to zero in spec, status not yet: admission still allows claims.
		quota(map[string]string{"raised.storageclass.storage.k8s.io/requests.storage": "0"},
			map[string]string{"raised.storageclass.storage.k8s.io/requests.storage": "10Gi"}),
		// New, status not filled yet: spec stands in.
		quota(map[string]string{"fresh.storageclass.storage.k8s.io/requests.storage": "0"}, nil),
		terminating,
		byAttributes,
	})
	want := map[string]bool{"lab": true, "replicated": true, "pending": true, "fresh": true}
	if len(got) != len(want) {
		t.Errorf("forbidden = %v, want %v", got, want)
	}
	for class := range want {
		if !got[class] {
			t.Errorf("forbidden = %v, want %s among them", got, class)
		}
	}
}
