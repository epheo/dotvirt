package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/restfactory"
)

// TestOptionsFilteredPerCaller pins the tenant boundary on the SA-read catalog:
// a namespaced entry outside the caller's visible set is served only when the
// caller could list that kind there itself (the shared golden-image case), so
// another tenant's namespace and network names never leave the process. The
// cached catalog must stay whole for the next caller.
func TestOptionsFilteredPerCaller(t *testing.T) {
	f := ssarFactory(t, func(_ *http.Request, review *authzv1.SelfSubjectAccessReview) bool {
		return review.Spec.ResourceAttributes.Namespace == "os-images"
	})
	s := NewServer(Deps{ClusterFactory: f, Draft: &fakeDraft{}, Bus: eventbus.New()})
	all := model.Options{
		Instancetypes: []model.Instancetype{{Name: "u1.small"}},
		OSImages: []model.OSImage{
			{Name: "fedora", Namespace: "os-images"},
			{Name: "private", Namespace: "team-b"},
		},
		Networks: []model.NetworkOption{
			{Name: "vlan10", Namespace: "team-a"},
			{Name: "secret-net", Namespace: "team-b"},
		},
	}
	s.options.Put("all", all)
	id := auth.Identity{Token: "tenant-token", Username: "tenant"}
	s.visible.Put(restfactory.TokenKey(id.Token), visibleSet{ns: map[string]bool{"team-a": true}, ver: s.rbacVersion()})

	r := httptest.NewRequest(http.MethodGet, "/api/options", nil)
	r = r.WithContext(auth.NewContext(r.Context(), id))
	rec := httptest.NewRecorder()
	s.handleOptions(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got model.Options
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OSImages) != 1 || got.OSImages[0].Name != "fedora" {
		t.Errorf("osImages = %+v, want only the shared golden image", got.OSImages)
	}
	if len(got.Networks) != 1 || got.Networks[0].Name != "vlan10" {
		t.Errorf("networks = %+v, want only the visible namespace's NAD", got.Networks)
	}
	if len(got.Instancetypes) != 1 {
		t.Errorf("cluster-scoped instancetypes must pass through unfiltered, got %+v", got.Instancetypes)
	}
	cached, _ := s.options.Get("all")
	if len(cached.OSImages) != 2 || len(cached.Networks) != 2 {
		t.Errorf("shared cache narrowed by one caller: %+v", cached)
	}
}
