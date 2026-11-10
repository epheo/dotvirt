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
)

// TestOptionsFilteredPerCaller pins the tenant boundary on the SA-read catalog:
// an entry is served only when the caller could list its kind there itself -
// a namespaced one outside the view too (the shared golden-image case), a
// cluster-scoped one cluster-wide. The cached catalog must stay whole for the
// next caller.
func TestOptionsFilteredPerCaller(t *testing.T) {
	f := ssarFactory(t, func(_ *http.Request, review *authzv1.SelfSubjectAccessReview) bool {
		a := review.Spec.ResourceAttributes
		return a.Namespace == "os-images" || a.Namespace == "list-only" && a.Resource == ssarDataSources.resource ||
			a.Verb == verbList && a.Resource == ssarInstancetypes.resource && a.Namespace == ""
	})
	s := NewServer(Deps{ClusterFactory: f, Reader: &fakeDraft{}, Draft: &fakeDraft{}, Bus: eventbus.New()})
	all := model.Options{
		Instancetypes:  []model.Instancetype{{Name: "u1.small"}},
		Preferences:    []model.Preference{{Name: "fedora"}},
		StorageClasses: []model.StorageClass{{Name: "fast"}},
		OSImages: []model.OSImage{
			{Name: "fedora", Namespace: "os-images"},
			{Name: "private", Namespace: "team-b"},
			// Listed, but booting it clones it, which the caller may not.
			{Name: "uncloneable", Namespace: "list-only"},
			// In a project namespace the caller views, not edits: the same.
			{Name: "viewed", Namespace: "team-a"},
		},
	}
	s.options.Put("all", all)
	id := auth.Identity{Token: "tenant-token", Username: "tenant"}
	seedVisibleWithReads(s, id.Token, map[string]bool{"team-a": true})

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
	if len(got.Instancetypes) != 1 {
		t.Errorf("instancetypes the caller lists = %+v", got.Instancetypes)
	}
	if got.Preferences == nil || len(got.Preferences) != 0 {
		t.Errorf("preferences the caller may not list = %+v", got.Preferences)
	}
	if got.StorageClasses == nil || len(got.StorageClasses) != 0 {
		t.Errorf("storage classes the caller may not list = %+v", got.StorageClasses)
	}
	cached, _ := s.options.Get("all")
	if len(cached.OSImages) != len(all.OSImages) {
		t.Errorf("shared cache narrowed by one caller: %+v", cached)
	}
}
