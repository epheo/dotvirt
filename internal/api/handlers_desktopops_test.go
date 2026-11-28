package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

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
		return token == tenantToken && a.Namespace == "team-a-dev" && a.Group == desktops.Group &&
			slices.ContainsFunc(rules, func(r rule) bool {
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
)

const claimsPath = "/apis/vdi.desku.be/v1alpha1/namespaces/team-a-dev/vdiclaims/"

// claimsAPI is the fake apiserver's claims: the UID each name holds now, and
// each write it took, failing the test on any made under a token other than
// the tenant's. It refuses a write held to another UID as the apiserver does:
// a JSON patch's failed test with a 422, a delete's failed precondition with
// a 409.
type claimsAPI struct {
	t *testing.T

	mu     sync.Mutex
	uids   map[string]string
	writes []string
}

func (c *claimsAPI) record(r *http.Request, what string) {
	if got := bearer(r); got != tenantToken {
		c.t.Errorf("%s %s ran as %q, want the caller's token", r.Method, r.URL.Path, got)
	}
	c.mu.Lock()
	c.writes = append(c.writes, what)
	c.mu.Unlock()
}

func (c *claimsAPI) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.writes)
}

func (c *claimsAPI) now(claim string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uids[claim]
}

func (c *claimsAPI) serve(fc *fakeCluster, claim string) {
	fc.on(http.MethodPatch, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json-patch+json" {
			c.t.Errorf("patch of %s as %q, want a JSON patch", claim, ct)
		}
		body, _ := io.ReadAll(r.Body)
		c.record(r, "patch "+claim+" "+string(body))
		var ops []struct {
			Op, Path string
			Value    any
		}
		_ = json.Unmarshal(body, &ops)
		if len(ops) == 0 || ops[0].Op != "test" || ops[0].Path != "/metadata/uid" || ops[0].Value != c.now(claim) {
			writeStatus(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid)
			return
		}
		writeObj(w, deskObject("VdiClaim", "team-a-dev", claim, map[string]any{"poolName": "lab"}, nil).Object)
	})
	fc.on(http.MethodDelete, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
		var opts metav1.DeleteOptions
		_ = json.NewDecoder(r.Body).Decode(&opts)
		uid := ""
		if opts.Preconditions != nil && opts.Preconditions.UID != nil {
			uid = string(*opts.Preconditions.UID)
		}
		c.record(r, "delete "+claim+" if "+uid)
		if uid != c.now(claim) {
			writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict)
			return
		}
		writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
	})
}

// actsServer serves the held desktops of src, its claims routed on a fake
// apiserver holding the UIDs the snapshot saw.
func actsServer(t *testing.T, may func(string, *authzv1.ResourceAttributes) bool, src *desktopsSource) (*Server, *fakeCluster, *claimsAPI) {
	t.Helper()
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = may
	api := &claimsAPI{t: t, uids: map[string]string{"lab-u": "uid-lab-u", "lab-bob": "uid-lab-bob"}}
	for claim := range api.uids {
		api.serve(fc, claim)
	}
	src.held = true
	s := desktopsServer(t, fc, src)
	s.tasks = tasks.New(s.bus)
	return s, fc, api
}

func act(s *Server, what, desktop, claim, uid string) *http.Response {
	body, _ := json.Marshal(claimRef{Claim: claim, UID: uid})
	return do(s, tenantToken, http.MethodPost, "/api/desktops/team-a-dev/"+desktop+"/"+what, string(body)).Result()
}

func wantAct(t *testing.T, res *http.Response, code int, says string) {
	t.Helper()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != code || !strings.Contains(string(body), says) {
		t.Fatalf("= %d %q, want %d saying %q", res.StatusCode, body, code, says)
	}
}

// Start and stop patch the holding claim's spec.running under the caller's
// token, never the desktop's, and release deletes the claim: each held to
// the claim's UID.
func TestClaimActsWriteTheClaimAsTheCaller(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), &desktopsSource{})
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNoContent, "")
	wantAct(t, act(s, "start", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNoContent, "")
	wantAct(t, act(s, "release", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNoContent, "")
	want := []string{
		`patch lab-bob [{"op":"test","path":"/metadata/uid","value":"uid-lab-bob"},{"op":"add","path":"/spec/running","value":false}]`,
		`patch lab-bob [{"op":"test","path":"/metadata/uid","value":"uid-lab-bob"},{"op":"add","path":"/spec/running","value":true}]`,
		"delete lab-bob if uid-lab-bob",
	}
	if got := api.seen(); !slices.Equal(got, want) {
		t.Fatalf("writes = %q, want %q", got, want)
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
		{"the claim verb granted by name", []rule{{"patch", "vdiclaims", "lab-bob"}, assignLab}, "stop", http.StatusNoContent, ""},
		{"the claim verb granted on another claim", []rule{{"patch", "vdiclaims", "lab-u"}, assignLab}, "stop", http.StatusForbidden, "patch vdiclaims lab-bob in team-a-dev"},
		{"no right on claims", []rule{assignLab}, "stop", http.StatusForbidden, "you may not stop bob's desktop: that takes patch vdiclaims lab-bob in team-a-dev, and assign or delete on vdipool lab"},
		{"no right on the pool", []rule{patchClaims}, "start", http.StatusForbidden, "and assign or delete on vdipool lab"},
		{"use is not enough on another's", []rule{patchClaims, useLab}, "stop", http.StatusForbidden, "assign or delete"},
		{"a release without the pool", []rule{deleteClaims}, "release", http.StatusForbidden, "you may not release bob's desktop"},
		{"assign on another pool", []rule{patchClaims, {"assign", "vdipools", "other"}}, "stop", http.StatusForbidden, "vdipool lab"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, api := actsServer(t, tenantMay(c.rules...), &desktopsSource{})
			wantAct(t, act(s, c.what, "lab-2", "lab-bob", "uid-lab-bob"), c.code, c.says)
			if c.code != http.StatusNoContent && len(api.seen()) > 0 {
				t.Fatalf("a refused act wrote %q", api.seen())
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
			s, _, _ := actsServer(t, tenantMay(c.rules...), &desktopsSource{})
			says := ""
			if c.code == http.StatusForbidden {
				says = "your desktop"
			}
			wantAct(t, act(s, c.what, "lab-1", "lab-u", "uid-lab-u"), c.code, says)
		})
	}
}

// An act names the claim the caller saw, by name and UID: another claim, or
// a desktop made one by one, is refused; one the caller may not list is not
// found.
func TestClaimActsAddressOnlyTheClaimTheCallerSaw(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), &desktopsSource{})
	wantAct(t, act(s, "release", "lab-2", "lab-u", "uid-lab-u"), http.StatusConflict, "claim lab-u no longer holds lab-2")
	wantAct(t, act(s, "release", "lab-2", "lab-bob", "uid-earlier"), http.StatusConflict, "claim lab-bob no longer holds lab-2")
	wantAct(t, act(s, "stop", "solo", "someone", "uid-someone"), http.StatusConflict, "solo starts and stops on its owner's claim; not acted on here yet")
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", ""), http.StatusBadRequest, "its uid")
	wantAct(t, act(s, "stop", "gone", "lab-bob", "uid-lab-bob"), http.StatusNotFound, "desktop team-a-dev/gone")
	// The desktops and claims of team-b-dev are not listed to the tenant.
	res := do(s, tenantToken, http.MethodPost, "/api/desktops/team-b-dev/lab-2/stop", `{"claim":"lab-bob","uid":"uid-lab-bob"}`).Result()
	wantAct(t, res, http.StatusNotFound, "desktop team-b-dev/lab-2")
	if len(api.seen()) > 0 {
		t.Fatalf("a refused act wrote %q", api.seen())
	}
}

// A desktop its pool now holds for another claim is refused, though the
// claim the caller names is still there by that name and UID.
func TestADesktopHeldForAnotherClaimIsRefused(t *testing.T) {
	src := &desktopsSource{}
	s, _, api := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), src)
	src.write(t, s, watch.Modified, heldDesktop("team-a-dev", "lab-2", "uid-someone-else"))
	wantAct(t, act(s, "release", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusConflict, "claim lab-bob no longer holds lab-2")
	if len(api.seen()) > 0 {
		t.Fatalf("a refused act wrote %q", api.seen())
	}
}

// bob deleted his claim lab-bob and someone made a new one under that name,
// which the snapshot has not heard of yet: the snapshot passes the act, and
// the apiserver refuses each write held to the UID the caller saw. The
// caller reads a conflict, never an internal error.
func TestAClaimNameReusedIsNeverActedOn(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), &desktopsSource{})
	api.mu.Lock()
	api.uids["lab-bob"] = "uid-lab-bob-anew"
	api.mu.Unlock()
	for _, what := range []string{"release", "stop", "start"} {
		wantAct(t, act(s, what, "lab-2", "lab-bob", "uid-lab-bob"), http.StatusConflict,
			"claim lab-bob is not the one you saw: it was deleted, and its name may be another person's now")
	}
	for _, w := range api.seen() {
		if !strings.Contains(w, `"value":"uid-lab-bob"}`) && !strings.HasSuffix(w, " if uid-lab-bob") {
			t.Errorf("a write not held to the claim seen: %s", w)
		}
	}
	if ops := s.tasks.Ops(); len(ops) != 3 || ops[0].OK {
		t.Fatalf("feed = %+v, want three failed acts", ops)
	}
}

// While the claims watch is down the snapshot may hold a claim since
// deleted: the act waits, saying why, and writes nothing.
func TestActsWaitWhileTheClaimsWatchIsDown(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), &desktopsSource{failClaims: true})
	wantAct(t, act(s, "release", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusServiceUnavailable,
		"the console's watch on the desktops or their claims is down")
	if len(api.seen()) > 0 {
		t.Fatalf("an act on a stale snapshot wrote %q", api.seen())
	}
}

// Listing the desktops without the claims shows no claim to act on.
func TestClaimActsNeedTheClaimsListed(t *testing.T) {
	s, _, api := actsServer(t, func(token string, a *authzv1.ResourceAttributes) bool {
		return token == tenantToken && a.Namespace == "team-a-dev" && (a.Verb != "list" || a.Resource == "vdiinstances")
	}, &desktopsSource{})
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNotFound, "no claim you may list holds desktop team-a-dev/lab-2")
	if len(api.seen()) > 0 {
		t.Fatalf("an act on an unlisted claim wrote %q", api.seen())
	}
}

// The apiserver's answer is final: a policy's denial reaches the caller as
// it said it.
func TestTheAdmissionDenialIsTheCallers(t *testing.T) {
	s, fc, _ := actsServer(t, tenantMay(deleteClaims, assignLab), &desktopsSource{})
	const denied = `vdiclaims.vdi.desku.be "lab-bob" is forbidden: ValidatingAdmissionPolicy 'vdiclaim-entitlement' with binding 'vdiclaim-entitlement' denied request: only its user may delete a claim`
	fc.on(http.MethodDelete, claimsPath+"lab-bob", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status: metav1.StatusFailure, Code: http.StatusForbidden, Reason: metav1.StatusReasonForbidden, Message: denied})
	})
	wantAct(t, act(s, "release", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusForbidden, "only its user may delete a claim")
	ops := s.tasks.Ops()
	if len(ops) != 1 || ops[0].OK {
		t.Fatalf("feed = %+v, want the failed release", ops)
	}
}

// Each act lands in the feed under the desktop's name, marked a desktop's,
// shown to whoever may list desktops there.
func TestDesktopActsLandInTheFeed(t *testing.T) {
	s, _, _ := actsServer(t, tenantMay(patchClaims, assignLab), &desktopsSource{})
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNoContent, "")
	rec := do(s, tenantToken, http.MethodGet, "/api/tasks", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.TaskEntry](t, rec)
	if len(got) != 1 || got[0].Verb != "Stop" || got[0].Name != "lab-2" || !got[0].Desktop || got[0].By != "u" || !got[0].OK || got[0].Project != "team-a" {
		t.Fatalf("feed = %+v", got)
	}
}

// The capability fields ask what the routes ask, the reads included, and
// answer only for a pool whose desktop the caller may see.
func TestDesktopCapsMirrorTheActs(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules []rule
		want  model.DesktopCaps
	}{
		{"everything", []rule{patchClaims, deleteClaims, assignLab},
			model.DesktopCaps{Power: true, PowerOwn: true, Release: true, ReleaseOwn: true}},
		{"a pool's user", []rule{patchClaims, deleteClaims, useLab},
			model.DesktopCaps{PowerOwn: true, ReleaseOwn: true}},
		{"the pool's deleter", []rule{patchClaims, deleteClaims, deleteLab},
			model.DesktopCaps{Power: true, PowerOwn: true, Release: true, ReleaseOwn: true}},
		{"claims alone", []rule{patchClaims, deleteClaims},
			model.DesktopCaps{ReleaseOwn: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := actsServer(t, tenantMay(c.rules...), &desktopsSource{})
			rec := do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-a-dev&pool=lab", "")
			wantStatus(t, rec, http.StatusOK)
			got := decodeBody[model.Capabilities](t, rec).Desktops
			c.want.Namespace, c.want.Pool = "team-a-dev", "lab"
			if got == nil || *got != c.want {
				t.Fatalf("caps = %+v, want %+v", got, c.want)
			}
		})
	}
	s, _, _ := actsServer(t, tenantMay(patchClaims, deleteClaims, assignLab), &desktopsSource{})
	// A namespace whose desktops the caller may not list offers nothing.
	rec := do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-b-dev&pool=lab", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decodeBody[model.Capabilities](t, rec).Desktops; *got != (model.DesktopCaps{Namespace: "team-b-dev", Pool: "lab"}) {
		t.Fatalf("caps where nothing is listed = %+v", got)
	}
	// A pool no desktop there is of is not answered, nor is no pool.
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-a-dev&pool=other", ""), http.StatusNotFound)
	wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/capabilities?desktops=team-a-dev", ""), http.StatusBadRequest)
}
