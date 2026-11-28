package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/tasks"
)

// rule is one right of the tenant: verb on resource in team-a-dev, on the
// object named when name is set.
type rule struct{ verb, resource, name string }

// tenantMay lets the tenant list the desktops and claims in team-a-dev, and
// hold each of rules there. Admin holds everything.
func tenantMay(rules ...rule) func(string, *authzv1.ResourceAttributes) bool {
	rules = append(rules, rule{"list", "vdiinstances", ""}, rule{"list", "vdiclaims", ""})
	return func(token string, a *authzv1.ResourceAttributes) bool {
		if token == adminToken {
			return true
		}
		if token != tenantToken || a.Namespace != "team-a-dev" {
			return false
		}
		group := desktops.Group
		if a.Resource == "pods" {
			group = ""
		}
		return a.Group == group && slices.ContainsFunc(rules, func(r rule) bool {
			return r.verb == a.Verb && r.resource == a.Resource && (r.name == "" || r.name == a.Name)
		})
	}
}

var (
	patchClaims  = rule{"patch", "vdiclaims", ""}
	deleteClaims = rule{"delete", "vdiclaims", ""}
	assignLab    = rule{"assign", "vdipools", "lab"}
	deleteLab    = rule{"delete", "vdipools", "lab"}
	useLab       = rule{"use", "vdipools", "lab"}
	listPods     = rule{"list", "pods", ""}
	deletePods   = rule{"delete", "pods", ""}
)

const claimsPath = "/apis/vdi.desku.be/v1alpha1/namespaces/team-a-dev/vdiclaims/"

// writes records each write the fake apiserver took on the claims and pods,
// failing the test on any made under a token other than the tenant's.
type writes struct {
	t     *testing.T
	calls []string
}

func (wr *writes) record(r *http.Request, what string) {
	if got := bearer(r); got != tenantToken {
		wr.t.Errorf("%s %s ran as %q, want the caller's token", r.Method, r.URL.Path, got)
	}
	wr.calls = append(wr.calls, what)
}

// actsServer serves the held desktops of desktopsSource, with the claim
// writes and the pod reads and deletes a desktop act makes routed.
func actsServer(t *testing.T, may func(string, *authzv1.ResourceAttributes) bool) (*Server, *fakeCluster, *writes) {
	t.Helper()
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = may
	wr := &writes{t: t}
	for _, claim := range []string{"lab-u", "lab-bob"} {
		fc.on(http.MethodPatch, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			wr.record(r, "patch "+claim+" "+string(body))
			writeObj(w, deskObject("VdiClaim", "team-a-dev", claim, map[string]any{"poolName": "lab"}, nil).Object)
		})
		fc.on(http.MethodDelete, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
			wr.record(r, "delete "+claim)
			writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
		})
	}
	s := desktopsServer(t, fc, &desktopsSource{held: true})
	s.tasks = tasks.New(s.bus)
	return s, fc, wr
}

func act(s *Server, what, desktop, claim string) *http.Response {
	body := `{"claim":"` + claim + `"}`
	return do(s, tenantToken, http.MethodPost, "/api/desktops/team-a-dev/"+desktop+"/"+what, body).Result()
}

func wantAct(t *testing.T, res *http.Response, code int, says string) {
	t.Helper()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != code || !strings.Contains(string(body), says) {
		t.Fatalf("= %d %q, want %d saying %q", res.StatusCode, body, code, says)
	}
}

// Start and stop patch the holding claim's spec.running under the caller's
// token, never the desktop's; release deletes the claim.
func TestClaimActsWriteTheClaimAsTheCaller(t *testing.T) {
	s, _, wr := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab))
	wantAct(t, act(s, "stop", "lab-2", "lab-bob"), http.StatusNoContent, "")
	wantAct(t, act(s, "start", "lab-2", "lab-bob"), http.StatusNoContent, "")
	wantAct(t, act(s, "release", "lab-2", "lab-bob"), http.StatusNoContent, "")
	want := []string{`patch lab-bob {"spec":{"running":false}}`, `patch lab-bob {"spec":{"running":true}}`, "delete lab-bob"}
	if !slices.Equal(wr.calls, want) {
		t.Fatalf("writes = %q, want %q", wr.calls, want)
	}
}

// Another's claim takes the claim verb and assign or delete on its pool by
// name, as DeskuBe's policy asks; one missing refuses before any write.
func TestAnothersClaimTakesThePoolsRight(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules []rule
		what  string
		code  int
		says  string
	}{
		{"assign opens a stop", []rule{patchClaims, assignLab}, "stop", http.StatusNoContent, ""},
		{"delete on the pool opens a stop", []rule{patchClaims, deleteLab}, "stop", http.StatusNoContent, ""},
		{"delete on the pool opens a release", []rule{deleteClaims, deleteLab}, "release", http.StatusNoContent, ""},
		{"no right on claims", []rule{assignLab}, "stop", http.StatusForbidden, "you may not stop bob's desktop: that takes patch vdiclaims in team-a-dev, and assign or delete on vdipool lab"},
		{"no right on the pool", []rule{patchClaims}, "start", http.StatusForbidden, "and assign or delete on vdipool lab"},
		{"use is not enough on another's", []rule{patchClaims, useLab}, "stop", http.StatusForbidden, "assign or delete"},
		{"a release without the pool", []rule{deleteClaims}, "release", http.StatusForbidden, "you may not release bob's desktop"},
		{"assign on another pool", []rule{patchClaims, {"assign", "vdipools", "other"}}, "stop", http.StatusForbidden, "vdipool lab"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, wr := actsServer(t, tenantMay(c.rules...))
			wantAct(t, act(s, c.what, "lab-2", "lab-bob"), c.code, c.says)
			if c.code != http.StatusNoContent && len(wr.calls) > 0 {
				t.Fatalf("a refused act wrote %q", wr.calls)
			}
		})
	}
}

// The caller's own claim: use on the pool opens a start or stop, and a
// release takes delete on the claims alone.
func TestOwnClaimTakesWhatThePolicyAsksOfItsUser(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules []rule
		what  string
		code  int
	}{
		{"use opens a stop", []rule{patchClaims, useLab}, "stop", http.StatusNoContent},
		{"assign opens a start", []rule{patchClaims, assignLab}, "start", http.StatusNoContent},
		{"a stop still takes the pool", []rule{patchClaims}, "stop", http.StatusForbidden},
		{"a release takes the claims alone", []rule{deleteClaims}, "release", http.StatusNoContent},
		{"a release takes delete on claims", []rule{patchClaims, useLab, assignLab}, "release", http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := actsServer(t, tenantMay(c.rules...))
			says := ""
			if c.code == http.StatusForbidden {
				says = "your desktop"
			}
			wantAct(t, act(s, c.what, "lab-1", "lab-u"), c.code, says)
		})
	}
}

// An act names the claim the caller saw: a desktop now held by another, or
// made one by one, is refused; one the caller may not list is not found.
func TestClaimActsAddressOnlyWhatTheCallerSaw(t *testing.T) {
	s, _, wr := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab))
	wantAct(t, act(s, "release", "lab-2", "lab-u"), http.StatusConflict, "claim lab-u no longer holds lab-2")
	wantAct(t, act(s, "stop", "solo", "someone"), http.StatusConflict, "solo is made one by one")
	wantAct(t, act(s, "stop", "lab-2", ""), http.StatusBadRequest, "name the claim")
	wantAct(t, act(s, "stop", "gone", "lab-bob"), http.StatusNotFound, "desktop team-a-dev/gone")
	// The desktops and claims of team-b-dev are not listed to the tenant.
	res := do(s, tenantToken, http.MethodPost, "/api/desktops/team-b-dev/lab-2/stop", `{"claim":"lab-bob"}`).Result()
	wantAct(t, res, http.StatusNotFound, "desktop team-b-dev/lab-2")
	if len(wr.calls) > 0 {
		t.Fatalf("a refused act wrote %q", wr.calls)
	}
}

// Listing the desktops without the claims shows no claim to act on.
func TestClaimActsNeedTheClaimsListed(t *testing.T) {
	s, _, wr := actsServer(t, func(token string, a *authzv1.ResourceAttributes) bool {
		return token == tenantToken && a.Namespace == "team-a-dev" && (a.Verb != "list" || a.Resource == "vdiinstances")
	})
	wantAct(t, act(s, "stop", "lab-2", "lab-bob"), http.StatusNotFound, "desktop team-a-dev/lab-2")
	if len(wr.calls) > 0 {
		t.Fatalf("an act on an unlisted claim wrote %q", wr.calls)
	}
}

// The apiserver's answer is final: a policy's denial reaches the caller as
// it said it.
func TestTheAdmissionDenialIsTheCallers(t *testing.T) {
	s, fc, _ := actsServer(t, tenantMay(deleteClaims, assignLab))
	const denied = `vdiclaims.vdi.desku.be "lab-bob" is forbidden: ValidatingAdmissionPolicy 'vdiclaim-entitlement' with binding 'vdiclaim-entitlement' denied request: only its user may delete a claim`
	fc.on(http.MethodDelete, claimsPath+"lab-bob", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status: metav1.StatusFailure, Code: http.StatusForbidden, Reason: metav1.StatusReasonForbidden, Message: denied})
	})
	wantAct(t, act(s, "release", "lab-2", "lab-bob"), http.StatusForbidden, "only its user may delete a claim")
	ops := s.tasks.Ops()
	if len(ops) != 1 || ops[0].OK {
		t.Fatalf("feed = %+v, want the failed release", ops)
	}
}

// A restart lists the desktop's pods by its label in its namespace, under
// the caller's token, and deletes only those.
func TestRestartDeletesOnlyTheDesktopsPods(t *testing.T) {
	s, fc, wr := actsServer(t, tenantMay(listPods, deletePods))
	pods := map[string]string{"lab-1-7d9f": "lab-1"}
	fc.on(http.MethodGet, "/api/v1/namespaces/team-a-dev/pods", func(w http.ResponseWriter, r *http.Request) {
		sel := r.URL.Query().Get("labelSelector")
		wr.record(r, "list "+sel)
		var items []corev1.Pod
		for name, desk := range pods {
			if sel == desktops.InstanceLabel+"="+desk {
				items = append(items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a-dev", UID: "uid-pod", Labels: map[string]string{desktops.InstanceLabel: desk}}})
			}
		}
		writeObj(w, corev1.PodList{TypeMeta: metav1.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: items})
	})
	fc.on(http.MethodDelete, "/api/v1/namespaces/team-a-dev/pods/lab-1-7d9f", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var opts metav1.DeleteOptions
		if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, &opts); err != nil {
			t.Errorf("delete options %q: %v", body, err)
		}
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-pod" {
			t.Errorf("the delete is not held to the pod listed: %+v", opts.Preconditions)
		}
		wr.record(r, "delete lab-1-7d9f")
		writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
	})

	wantAct(t, act(s, "restart", "lab-1", ""), http.StatusNoContent, "")
	// A desktop made one by one restarts the same way; this one runs no pod.
	wantAct(t, act(s, "restart", "solo", ""), http.StatusConflict, "solo runs no pod to restart")
	want := []string{"list vdi.desku.be/instance=lab-1", "delete lab-1-7d9f", "list vdi.desku.be/instance=solo"}
	if !slices.Equal(wr.calls, want) {
		t.Fatalf("calls = %q, want %q", wr.calls, want)
	}
	for _, c := range fc.seen() {
		if strings.Contains(c, "/pods") && !strings.Contains(c, "/namespaces/team-a-dev/") || strings.Contains(c, "deployments") {
			t.Errorf("a restart reached %s", c)
		}
	}
}

func TestRestartTakesListAndDeleteOnPods(t *testing.T) {
	for _, rules := range [][]rule{{listPods}, {deletePods}} {
		s, fc, _ := actsServer(t, tenantMay(rules...))
		wantAct(t, act(s, "restart", "lab-1", ""), http.StatusForbidden, "you may not")
		for _, c := range fc.seen() {
			if strings.Contains(c, "/pods") {
				t.Errorf("a refused restart reached %s", c)
			}
		}
	}
}

// Each act lands in the feed under the desktop's name, marked a desktop's,
// shown to whoever may list desktops there.
func TestDesktopActsLandInTheFeed(t *testing.T) {
	s, _, _ := actsServer(t, tenantMay(patchClaims, assignLab))
	wantAct(t, act(s, "stop", "lab-2", "lab-bob"), http.StatusNoContent, "")
	rec := do(s, tenantToken, http.MethodGet, "/api/tasks", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.TaskEntry](t, rec)
	if len(got) != 1 || got[0].Verb != "Stop" || got[0].Name != "lab-2" || !got[0].Desktop || got[0].By != "u" || !got[0].OK || got[0].Project != "team-a" {
		t.Fatalf("feed = %+v", got)
	}
}

// The capability fields ask what the routes ask, the reads included.
func TestDesktopCapsMirrorTheActs(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules []rule
		pool  string
		want  model.DesktopCaps
	}{
		{"everything", []rule{patchClaims, deleteClaims, assignLab, listPods, deletePods}, "lab",
			model.DesktopCaps{Restart: true, Power: true, PowerOwn: true, Release: true, ReleaseOwn: true}},
		{"a pool's user", []rule{patchClaims, deleteClaims, useLab}, "lab",
			model.DesktopCaps{PowerOwn: true, ReleaseOwn: true}},
		{"the pool's deleter", []rule{patchClaims, deleteClaims, deleteLab}, "lab",
			model.DesktopCaps{Power: true, PowerOwn: true, Release: true, ReleaseOwn: true}},
		{"claims alone", []rule{patchClaims, deleteClaims}, "lab",
			model.DesktopCaps{ReleaseOwn: true}},
		{"restart without delete", []rule{listPods}, "lab", model.DesktopCaps{}},
		{"made one by one", []rule{patchClaims, deleteClaims, assignLab, listPods, deletePods}, "",
			model.DesktopCaps{Restart: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := actsServer(t, tenantMay(c.rules...))
			rec := do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-a-dev&pool="+c.pool, "")
			wantStatus(t, rec, http.StatusOK)
			got := decodeBody[model.Capabilities](t, rec).Desktops
			c.want.Namespace, c.want.Pool = "team-a-dev", c.pool
			if got == nil || *got != c.want {
				t.Fatalf("caps = %+v, want %+v", got, c.want)
			}
		})
	}
	// A namespace whose desktops the caller may not list offers nothing.
	s, _, _ := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab, listPods, deletePods))
	rec := do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-b-dev&pool=lab", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.Capabilities](t, rec).Desktops; *got != (model.DesktopCaps{Namespace: "team-b-dev", Pool: "lab"}) {
		t.Fatalf("caps where nothing is listed = %+v", got)
	}
}
