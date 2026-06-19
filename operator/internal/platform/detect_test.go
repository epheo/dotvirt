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
