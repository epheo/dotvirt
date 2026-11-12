package deps

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

func list(gv string, resources ...string) *metav1.APIResourceList {
	l := &metav1.APIResourceList{GroupVersion: gv}
	for _, r := range resources {
		l.APIResources = append(l.APIResources, metav1.APIResource{Name: r})
	}
	return l
}

// The OAuth API counts as there while its aggregated apiserver fails
// discovery: an OpenShift upgrade must not flip SSO off and on again.
func TestClassifyOpenShiftOAuth(t *testing.T) {
	argo := list("argoproj.io/v1alpha1", "applications")
	down := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "oauth.openshift.io", Version: "v1"}: errors.New("the server is currently unable to handle the request"),
	}}
	other := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("the server is currently unable to handle the request"),
	}}
	for _, c := range []struct {
		name  string
		lists []*metav1.APIResourceList
		err   error
		want  bool
	}{
		{"served", []*metav1.APIResourceList{argo, list("oauth.openshift.io/v1", "oauthclients")}, nil, true},
		{"discovery failed", []*metav1.APIResourceList{argo}, down, true},
		{"absent", []*metav1.APIResourceList{argo}, nil, false},
		{"absent, another group failed", []*metav1.APIResourceList{argo}, other, false},
	} {
		if got := classify(c.lists, c.err).OpenShiftOAuth; got != c.want {
			t.Errorf("%s: OpenShiftOAuth = %v, want %v", c.name, got, c.want)
		}
	}
}
