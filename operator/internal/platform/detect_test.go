package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// discoveryServer serves the aggregated /apis group list the discovery client
// reads, advertising the given group names. /api (legacy core) is served empty.
func discoveryServer(t *testing.T, groupNames ...string) *httptest.Server {
	t.Helper()
	groups := metav1.APIGroupList{}
	for _, n := range groupNames {
		groups.Groups = append(groups.Groups, metav1.APIGroup{Name: n})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis":
			_ = json.NewEncoder(w).Encode(groups)
		case "/api":
			_ = json.NewEncoder(w).Encode(metav1.APIVersions{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name   string
		groups []string
		want   Platform
	}{
		{"openshift via route group", []string{"apps", "route.openshift.io"}, OpenShift},
		{"openshift via config group", []string{"config.openshift.io"}, OpenShift},
		{"vanilla kubernetes", []string{"apps", "batch"}, Kubernetes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := discoveryServer(t, c.groups...)
			defer srv.Close()
			got, err := Detect(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != c.want {
				t.Errorf("Detect = %q, want %q", got, c.want)
			}
		})
	}
}

// Only a full OpenShift serves clusterversions; MicroShift serves other
// config.openshift.io kinds, plain Kubernetes not the group at all.
func TestServesClusterVersion(t *testing.T) {
	for _, c := range []struct {
		name      string
		resources []string // nil: config.openshift.io/v1 not served
		want      bool
	}{
		{"openshift", []string{"clusterversions", "infrastructures"}, true},
		{"microshift", []string{"certmanagers"}, false},
		{"kubernetes", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apis/config.openshift.io/v1" || c.resources == nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				list := metav1.APIResourceList{GroupVersion: "config.openshift.io/v1"}
				for _, n := range c.resources {
					list.APIResources = append(list.APIResources, metav1.APIResource{Name: n})
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(list)
			}))
			defer srv.Close()
			got, err := ServesClusterVersion(&rest.Config{Host: srv.URL})
			if err != nil || got != c.want {
				t.Errorf("ServesClusterVersion = %v, %v, want %v", got, err, c.want)
			}
		})
	}
}
