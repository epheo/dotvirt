package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/model"
)

const platformRepo = "https://forge/platform.git"

// deschedOnly may create KubeDeschedulers but not MachineConfigs: the DRS
// author who must not reboot the worker pool.
func deschedOnly(token string, a *authzv1.ResourceAttributes) bool {
	return token == adminToken || (token == "drs-token" && a.Resource == "kubedeschedulers")
}

func TestDRSView(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = deschedOnly
	d := &planeDraft{
		drs:      model.DRSGitState{Configured: true, PSIConfigured: true},
		drsDraft: model.DRSDraftState{DisableStaged: true},
	}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	rec := do(s, adminToken, http.MethodGet, "/api/drs", "")
	wantStatus(t, rec, http.StatusOK)
	v := decodeBody[model.DRSView](t, rec)
	if !v.Configured || !v.PSIConfigured || !v.CanManage || !v.CanPSI || v.Draft == nil || !v.Draft.DisableStaged || v.Warning != "" {
		t.Errorf("admin view = %+v", v)
	}

	rec = do(s, "drs-token", http.MethodGet, "/api/drs", "")
	wantStatus(t, rec, http.StatusOK)
	if v := decodeBody[model.DRSView](t, rec); !v.CanManage || v.CanPSI {
		t.Errorf("drs author view = %+v, want manage without PSI", v)
	}

	// A tenant reads the committed state but never a draft.
	rec = do(s, tenantToken, http.MethodGet, "/api/drs", "")
	wantStatus(t, rec, http.StatusOK)
	if v := decodeBody[model.DRSView](t, rec); v.CanManage || v.Draft != nil || !v.Configured {
		t.Errorf("tenant view = %+v", v)
	}

	// The git plane failing degrades to a warning, never a 500.
	d.drsErr = errors.New("forge down")
	rec = do(s, adminToken, http.MethodGet, "/api/drs", "")
	wantStatus(t, rec, http.StatusOK)
	if v := decodeBody[model.DRSView](t, rec); v.Warning == "" || v.Configured {
		t.Errorf("degraded view = %+v", v)
	}
}

// Without a platform repo the view is only the live plane.
func TestDRSViewNoPlatform(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	rec := do(s, adminToken, http.MethodGet, "/api/drs", "")
	wantStatus(t, rec, http.StatusOK)
	if v := decodeBody[model.DRSView](t, rec); v.CanManage || v.Configured {
		t.Errorf("view = %+v", v)
	}
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/drs", `{}`), http.StatusServiceUnavailable)
}

func TestDRSEnableDisable(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssar = deschedOnly
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/drs", `{`), http.StatusBadRequest)
	wantStatus(t, do(s, tenantToken, http.MethodPost, "/api/drs", `{}`), http.StatusForbidden)
	wantStatus(t, do(s, tenantToken, http.MethodDelete, "/api/drs", ""), http.StatusForbidden)
	// The PSI file reboots workers: it needs its own authority.
	wantStatus(t, do(s, "drs-token", http.MethodPost, "/api/drs", `{"installPSI":true}`), http.StatusForbidden)
	d.none(t)

	wantStatus(t, do(s, "drs-token", http.MethodPost, "/api/drs", `{"installPSI":false,"mode":"Automatic"}`), http.StatusOK)
	d.only(t, "StageEnableDRS platform")
	if !strings.Contains(string(d.raw), `"mode":"Automatic"`) {
		t.Errorf("staged spec = %s, want the raw body passed through", d.raw)
	}
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/drs", `{"installPSI":true}`), http.StatusOK)
	d.only(t, "StageEnableDRS platform")
	wantStatus(t, do(s, "drs-token", http.MethodDelete, "/api/drs", ""), http.StatusOK)
	d.only(t, "StageDisableDRS platform")
}

func TestTemplatesList(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssrr = func(token, ns string) []authzv1.ResourceRule {
		if token == tenantToken && ns == "team-a-dev" {
			return []authzv1.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}}
		}
		return nil
	}
	d := &planeDraft{templates: map[string][]model.Template{
		"platform": {{Name: "rhel9", Library: "platform"}},
		"team-a":   {{Name: "web", Library: "team-a"}},
		"orphans":  {{Name: "never", Library: "orphans"}},
	}}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	names := func(token string) []string {
		rec := do(s, token, http.MethodGet, "/api/templates", "")
		wantStatus(t, rec, http.StatusOK)
		var out []string
		for _, tpl := range decodeBody[model.TemplateList](t, rec).Templates {
			out = append(out, tpl.Library+"/"+tpl.Name)
		}
		return out
	}
	// The repoless project has no library; the platform library lists for all.
	if got := strings.Join(names(adminToken), ","); got != "platform/rhel9,team-a/web" {
		t.Errorf("admin libraries = %s", got)
	}
	if got := strings.Join(names(tenantToken), ","); got != "platform/rhel9,team-a/web" {
		t.Errorf("tenant libraries = %s", got)
	}
	if got := names("stranger"); len(got) != 1 {
		t.Errorf("stranger libraries = %v, want only the shared one", got)
	}
}

func TestTemplatesListEmptyIsArray(t *testing.T) {
	fc := newFakeCluster(t)
	s := fc.server(nil, Config{})
	rec := do(s, "stranger", http.MethodGet, "/api/templates", "")
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"templates":[]}` {
		t.Errorf("body = %s, want an empty array", rec.Body.String())
	}
}

func TestDeployTemplate(t *testing.T) {
	fc := newFakeCluster(t)
	fc.namespaces = append(fc.namespaces, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "team-b-dev", Labels: map[string]string{"dotvirt.io/project": "team-b"}, Annotations: map[string]string{"dotvirt.io/repo": "https://forge/team-b.git"},
	}})
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	for _, c := range []struct {
		body string
		want int
		call string
	}{
		{`{"template":"web"}`, http.StatusBadRequest, ""},
		{`{"namespace":"elsewhere","template":"web"}`, http.StatusNotFound, ""},
		{`{"namespace":"team-a-dev","library":"ghost","template":"web"}`, http.StatusNotFound, ""},
		{`{"namespace":"team-a-dev","template":"web"}`, http.StatusOK, "StageDeployFromTemplate team-a,team-a"},
		{`{"namespace":"team-a-dev","library":"team-a","template":"web"}`, http.StatusOK, "StageDeployFromTemplate team-a,team-a"},
		{`{"namespace":"team-a-dev","library":"platform","template":"rhel9"}`, http.StatusOK, "StageDeployFromTemplate team-a,platform"},
		{`{"namespace":"team-a-dev","library":"team-b","template":"db"}`, http.StatusOK, "StageDeployFromTemplate team-a,team-b"},
	} {
		rec := do(s, adminToken, http.MethodPost, "/api/templates/deploy", c.body)
		if rec.Code != c.want {
			t.Errorf("deploy %s = %d %q, want %d", c.body, rec.Code, rec.Body.String(), c.want)
		}
		if c.call == "" {
			d.none(t)
		} else {
			d.only(t, c.call)
		}
	}
}

func TestDeployTemplateNoPlatform(t *testing.T) {
	fc := newFakeCluster(t)
	d := &planeDraft{}
	s := fc.server(d, Config{})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/templates/deploy", `{"namespace":"team-a-dev","library":"platform"}`), http.StatusServiceUnavailable)
	d.none(t)
}

// Editing a library is writing to it: membership for a project library, the
// template create authority for the shared one.
func TestUpdateTemplate(t *testing.T) {
	fc := newFakeCluster(t)
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	for _, c := range []struct {
		token, body string
		want        int
		call        string
	}{
		{adminToken, `{"library":"team-a","name":"web"}`, http.StatusBadRequest, ""},
		{adminToken, `[]`, http.StatusBadRequest, ""},
		{tenantToken, `{"library":"platform","name":"rhel9","yaml":"x"}`, http.StatusForbidden, ""},
		{tenantToken, `{"library":"team-a","name":"web","yaml":"x"}`, http.StatusNotFound, ""},
		{adminToken, `{"library":"platform","name":"rhel9","yaml":"x"}`, http.StatusOK, "StageUpdateTemplate platform"},
		{adminToken, `{"library":"team-a","name":"web","yaml":"x"}`, http.StatusOK, "StageUpdateTemplate team-a"},
	} {
		rec := do(s, c.token, http.MethodPut, "/api/templates", c.body)
		if rec.Code != c.want {
			t.Errorf("%s update %s = %d %q, want %d", c.token, c.body, rec.Code, rec.Body.String(), c.want)
		}
		if c.call == "" {
			d.none(t)
		} else {
			d.only(t, c.call)
		}
	}
}

func TestSaveTemplate(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssrr = func(token, ns string) []authzv1.ResourceRule {
		if token == tenantToken && ns == "team-a-dev" {
			return []authzv1.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}}
		}
		return nil
	}
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	for _, c := range []struct {
		token, body string
		want        int
		call        string
	}{
		{adminToken, `{"sourceNamespace":"team-a-dev"}`, http.StatusBadRequest, ""},
		{adminToken, `{"sourceNamespace":"elsewhere","sourceName":"web"}`, http.StatusNotFound, ""},
		{adminToken, `{"sourceNamespace":"team-a-dev","sourceName":"web","library":"orphans"}`, http.StatusBadRequest, ""},
		{tenantToken, `{"sourceNamespace":"team-a-dev","sourceName":"web","library":"platform"}`, http.StatusForbidden, ""},
		{tenantToken, `{"sourceNamespace":"team-a-dev","sourceName":"web"}`, http.StatusOK, "StageSaveTemplate team-a,team-a"},
		{adminToken, `{"sourceNamespace":"team-a-dev","sourceName":"web","library":"team-a"}`, http.StatusOK, "StageSaveTemplate team-a,team-a"},
		{adminToken, `{"sourceNamespace":"team-a-dev","sourceName":"web","library":"platform"}`, http.StatusOK, "StageSaveTemplate platform,team-a"},
	} {
		rec := do(s, c.token, http.MethodPost, "/api/templates", c.body)
		if rec.Code != c.want {
			t.Errorf("%s save %s = %d %q, want %d", c.token, c.body, rec.Code, rec.Body.String(), c.want)
		}
		if c.call == "" {
			d.none(t)
		} else {
			d.only(t, c.call)
		}
	}
}

func TestCreateNamespace(t *testing.T) {
	fc := newFakeCluster(t)
	fc.ssrr = func(token, ns string) []authzv1.ResourceRule {
		if token == tenantToken && ns == "team-a-dev" {
			return []authzv1.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{"kubevirt.io"}, Resources: []string{"virtualmachines"}}}
		}
		return nil
	}
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	for _, c := range []struct {
		token, body string
		want        int
	}{
		{adminToken, `{"name":"team-a-test"}`, http.StatusBadRequest},
		{adminToken, `{"project":"ghost","name":"x"}`, http.StatusNotFound},
		// Seeing the project is not authority to land a Namespace.
		{tenantToken, `{"project":"team-a","name":"x"}`, http.StatusForbidden},
	} {
		if rec := do(s, c.token, http.MethodPost, "/api/namespaces", c.body); rec.Code != c.want {
			t.Errorf("%s create %s = %d %q, want %d", c.token, c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
	d.none(t)

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/namespaces", `{"project":"team-a","name":"team-a-test"}`), http.StatusOK)
	d.only(t, "StageCreateNamespace platform,team-a")
	if !strings.Contains(string(d.raw), `"name":"team-a-test"`) {
		t.Errorf("staged spec = %s", d.raw)
	}
}

func TestCreateProject(t *testing.T) {
	fc := newFakeCluster(t)
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	for _, c := range []struct {
		token, body string
		want        int
	}{
		{adminToken, `{}`, http.StatusBadRequest},
		{tenantToken, `{"name":"team-c"}`, http.StatusForbidden},
		// An existing project is adopted, never re-created over its repo.
		{adminToken, `{"name":"team-a"}`, http.StatusConflict},
		{adminToken, `{"name":"orphans"}`, http.StatusConflict},
	} {
		if rec := do(s, c.token, http.MethodPost, "/api/projects", c.body); rec.Code != c.want {
			t.Errorf("%s create %s = %d %q, want %d", c.token, c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
	d.none(t)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects", `{"name":"team-c"}`), http.StatusOK)
	d.only(t, "StageCreateProject platform")
}

func TestAdoptProject(t *testing.T) {
	fc := newFakeCluster(t)
	d := &planeDraft{}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	wantStatus(t, do(s, tenantToken, http.MethodPost, "/api/projects/orphans/adopt", ""), http.StatusForbidden)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/ghost/adopt", ""), http.StatusNotFound)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/orphans/adopt", `{"owners":`), http.StatusBadRequest)
	d.none(t)

	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/orphans/adopt", ""), http.StatusOK)
	d.only(t, "AdoptProject platform,orphans ")
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/orphans/adopt", `{"owners":["alice","bob"]}`), http.StatusOK)
	d.only(t, "AdoptProject platform,orphans alice+bob")
}

func TestReleaseProject(t *testing.T) {
	fc := newFakeCluster(t)
	var patch string
	fc.on(http.MethodPatch, "/api/v1/namespaces/orphans-ns", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		patch = string(b)
		writeObj(w, corev1.Namespace{TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "orphans-ns"}})
	})
	d := &planeDraft{releaseStaged: []string{"namespaces/orphans-ns.yaml"}, releaseResidue: []string{"orphans-ns"}}
	s := fc.server(d, Config{PlatformRepo: platformRepo})

	wantStatus(t, do(s, tenantToken, http.MethodPost, "/api/projects/orphans/release", ""), http.StatusForbidden)
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/ghost/release", ""), http.StatusNotFound)
	// Release covers only the repoless dead end, never a healthy tenant.
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/team-a/release", ""), http.StatusConflict)
	d.none(t)

	rec := do(s, adminToken, http.MethodPost, "/api/projects/orphans/release", "")
	wantStatus(t, rec, http.StatusOK)
	d.only(t, "ReleaseDeclared platform,orphans")
	res := decodeBody[model.ReleaseResult](t, rec)
	if len(res.Released) != 1 || res.Released[0] != "orphans-ns" || len(res.Staged) != 1 || res.Draft == nil {
		t.Errorf("release = %+v", res)
	}
	if patch != `{"metadata":{"labels":{"dotvirt.io/project":null},"annotations":{"dotvirt.io/repo":null}}}` {
		t.Errorf("namespace patch = %s", patch)
	}
}

// The residue strip runs under the caller's token: its refusal is the
// caller's 403, with what was staged left in the draft.
func TestReleaseProjectStripForbidden(t *testing.T) {
	fc := newFakeCluster(t)
	fc.on(http.MethodPatch, "/api/v1/namespaces/orphans-ns", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
	})
	d := &planeDraft{releaseResidue: []string{"orphans-ns"}}
	s := fc.server(d, Config{PlatformRepo: platformRepo})
	wantStatus(t, do(s, adminToken, http.MethodPost, "/api/projects/orphans/release", ""), http.StatusForbidden)
}
