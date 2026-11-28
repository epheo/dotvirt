package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
	patchClaims   = rule{"patch", "vdiclaims", ""}
	deleteClaims  = rule{"delete", "vdiclaims", ""}
	readDesktops   = rule{"get", "vdiinstances", ""}
	patchDesktops = rule{"patch", "vdiinstances", ""}
	assignLab     = rule{"assign", "vdipools", "lab"}
	deleteLab     = rule{"delete", "vdipools", "lab"}
	useLab        = rule{"use", "vdipools", "lab"}
)

const (
	claimsPath   = "/apis/vdi.desku.be/v1alpha1/namespaces/team-a-dev/vdiclaims/"
	desktopsPath = "/apis/vdi.desku.be/v1alpha1/namespaces/team-a-dev/vdiinstances/"
)

// deskubeAPI is the fake apiserver's claims and desktops: the UID each name
// holds now, each desktop's owner and resourceVersion, and each read of a
// desktop and write it took, failing the test on any made under a token other
// than the tenant's. It refuses a write held to another object as the
// apiserver does: a patch carrying another metadata.uid with a 422 naming
// that field, another resourceVersion with a 409, a delete's failed
// precondition with a 409.
type deskubeAPI struct {
	t *testing.T

	mu     sync.Mutex
	uids   map[string]string // claims
	desks  map[string]string // desktops
	owners map[string]string
	rvs    map[string]int
	// movesOnRead names the desktops another write lands on right after
	// each read: the patch that follows carries a resourceVersion gone by.
	movesOnRead map[string]bool
	writes      []string
}

func (c *deskubeAPI) record(r *http.Request, what string) {
	if got := bearer(r); got != tenantToken {
		c.t.Errorf("%s %s ran as %q, want the caller's token", r.Method, r.URL.Path, got)
	}
	c.mu.Lock()
	c.writes = append(c.writes, what)
	c.mu.Unlock()
}

func (c *deskubeAPI) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.writes)
}

func (c *deskubeAPI) now(uids map[string]string, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return uids[name]
}

// writeInvalid answers a 422 as the apiserver's update validation does, its
// one cause on field.
func writeInvalid(w http.ResponseWriter, kind, name, field, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Code: http.StatusUnprocessableEntity, Reason: metav1.StatusReasonInvalid,
		Message: kind + `.vdi.desku.be "` + name + `" is invalid: ` + field + ": " + why,
		Details: &metav1.StatusDetails{Name: name, Group: desktops.Group, Kind: kind, Causes: []metav1.StatusCause{
			{Type: metav1.CauseTypeFieldValueInvalid, Field: field, Message: why},
		}},
	})
}

// mergePatch reads a merge patch of r, recorded as what, and answers the 422
// of a metadata.uid other than the one name holds now. ok=false means it
// answered.
func (c *deskubeAPI) mergePatch(w http.ResponseWriter, r *http.Request, kind, name, what string, uids map[string]string) bool {
	if ct := r.Header.Get("Content-Type"); ct != "application/merge-patch+json" {
		c.t.Errorf("patch of %s as %q, want a merge patch", name, ct)
	}
	body, _ := io.ReadAll(r.Body)
	c.record(r, what+" "+name+" "+string(body))
	var patch struct {
		Metadata struct {
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(body, &patch)
	c.mu.Lock()
	rv := strconv.Itoa(c.rvs[name])
	c.mu.Unlock()
	switch {
	case patch.Metadata.ResourceVersion != "" && patch.Metadata.ResourceVersion != rv:
		writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict)
		return false
	case patch.Metadata.UID != c.now(uids, name):
		writeInvalid(w, kind, name, "metadata.uid", `Invalid value: "`+patch.Metadata.UID+`": field is immutable`)
		return false
	}
	return true
}

func (c *deskubeAPI) serve(fc *fakeCluster, claim string) {
	fc.on(http.MethodPatch, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
		if c.mergePatch(w, r, "VdiClaim", claim, "patch", c.uids) {
			writeObj(w, deskObject("VdiClaim", "team-a-dev", claim, map[string]any{"poolName": "lab"}, nil).Object)
		}
	})
	fc.on(http.MethodDelete, claimsPath+claim, func(w http.ResponseWriter, r *http.Request) {
		var opts metav1.DeleteOptions
		_ = json.NewDecoder(r.Body).Decode(&opts)
		uid := ""
		if opts.Preconditions != nil && opts.Preconditions.UID != nil {
			uid = string(*opts.Preconditions.UID)
		}
		c.record(r, "delete "+claim+" if "+uid)
		if uid != c.now(c.uids, claim) {
			writeStatus(w, http.StatusConflict, metav1.StatusReasonConflict)
			return
		}
		writeObj(w, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess})
	})
}

// desktop is the desktop name as the fake apiserver holds it now.
func (c *deskubeAPI) desktop(name string) *unstructured.Unstructured {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := owned(deskInstance("team-a-dev", name, "Ready"), c.owners[name])
	d.SetUID(types.UID(c.desks[name]))
	d.SetResourceVersion(strconv.Itoa(c.rvs[name]))
	return d
}

func (c *deskubeAPI) serveDesktop(fc *fakeCluster, desktop string) {
	fc.on(http.MethodGet, desktopsPath+desktop, func(w http.ResponseWriter, r *http.Request) {
		c.record(r, "get "+desktop)
		writeObj(w, c.desktop(desktop).Object)
		c.mu.Lock()
		if c.movesOnRead[desktop] {
			c.rvs[desktop]++
		}
		c.mu.Unlock()
	})
	fc.on(http.MethodPatch, desktopsPath+desktop, func(w http.ResponseWriter, r *http.Request) {
		if c.mergePatch(w, r, "VdiInstance", desktop, "restart", c.desks) {
			writeObj(w, c.desktop(desktop).Object)
		}
	})
}

// owned is u with owner as its spec.owner.
func owned(u *unstructured.Unstructured, owner string) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(u.Object, owner, "spec", "owner")
	return u
}

// actsServer serves the held desktops of src, its claims and desktops routed
// on a fake apiserver holding the UIDs and owners the snapshot saw.
func actsServer(t *testing.T, may func(string, *authzv1.ResourceAttributes) bool, src *desktopsSource) (*Server, *fakeCluster, *deskubeAPI) {
	t.Helper()
	fc := newFakeCluster(t)
	fc.readsVMs = tenantSeesTeams
	fc.ssar = may
	api := &deskubeAPI{t: t,
		uids:        map[string]string{"lab-u": "uid-lab-u", "lab-bob": "uid-lab-bob"},
		desks:       map[string]string{"lab-1": "uid-desk-lab-1", "lab-2": "uid-desk-lab-2", "solo": "uid-desk-solo"},
		owners:      map[string]string{"lab-1": "lab-1", "lab-2": "lab-2", "solo": "solo"},
		rvs:         map[string]int{"lab-1": 7, "lab-2": 7, "solo": 7},
		movesOnRead: map[string]bool{},
	}
	for claim := range api.uids {
		api.serve(fc, claim)
	}
	for desk := range api.desks {
		api.serveDesktop(fc, desk)
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

// restart asks to restart desktop as the tenant saw it: its UID, and its owner,
// which every fixture desktop names after itself.
func restart(s *Server, desktop, uid string) *http.Response {
	return restartFor(s, desktop, uid, desktop)
}

func restartFor(s *Server, desktop, uid, owner string) *http.Response {
	body, _ := json.Marshal(desktopRef{UID: uid, Owner: owner})
	return do(s, tenantToken, http.MethodPost, "/api/desktops/team-a-dev/"+desktop+"/restart", string(body)).Result()
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
		`patch lab-bob {"metadata":{"uid":"uid-lab-bob"},"spec":{"running":false}}`,
		`patch lab-bob {"metadata":{"uid":"uid-lab-bob"},"spec":{"running":true}}`,
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
		if !strings.Contains(w, `{"uid":"uid-lab-bob"}`) && !strings.HasSuffix(w, " if uid-lab-bob") {
			t.Errorf("a write not held to the claim seen: %s", w)
		}
	}
	if ops := s.tasks.Ops(); len(ops) != 3 || ops[0].OK {
		t.Fatalf("feed = %+v, want three failed acts", ops)
	}
}

// A patch the apiserver refuses as invalid on another field is no other
// claim: the caller reads what the apiserver said of it.
func TestAnInvalidPatchIsNotAnotherClaim(t *testing.T) {
	s, fc, _ := actsServer(t, tenantMay(patchClaims, assignLab), &desktopsSource{})
	fc.on(http.MethodPatch, claimsPath+"lab-bob", func(w http.ResponseWriter, _ *http.Request) {
		writeInvalid(w, "VdiClaim", "lab-bob", "spec.running", "Invalid value: \"string\": spec.running in body must be of type boolean")
	})
	res := act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "spec.running in body must be of type boolean") ||
		strings.Contains(string(body), "another person's") {
		t.Fatalf("= %d %q, want the apiserver's own words", res.StatusCode, body)
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
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNotFound, "desktop team-a-dev/lab-2: the claims there, which this act reads, are not listed to you")
	if len(api.seen()) > 0 {
		t.Fatalf("an act on an unlisted claim wrote %q", api.seen())
	}
}

// A restart reads no claim: a caller who lists the desktops and not the
// claims restarts, and asking its rights of a claim, there or made up, tells
// it nothing of the claims.
func TestADesktopListerRestartsWithoutTheClaims(t *testing.T) {
	s, _, api := actsServer(t, func(token string, a *authzv1.ResourceAttributes) bool {
		return token == tenantToken && a.Namespace == "team-a-dev" && a.Resource == "vdiinstances"
	}, &desktopsSource{})
	wantAct(t, restart(s, "lab-2", "uid-desk-lab-2"), http.StatusNoContent, "")
	if got := api.seen(); len(got) != 2 {
		t.Fatalf("writes = %q, want one read and one patch", got)
	}
	want := model.DesktopCaps{Namespace: "team-a-dev", Desktop: "lab-2", Restart: true}
	for _, claim := range []string{"", "lab-bob", "made-up"} {
		if got := desktopCaps(t, s, "desktops=team-a-dev&desktop=lab-2&claim="+claim); *got != want {
			t.Fatalf("caps asked of claim %q = %+v, want %+v", claim, got, want)
		}
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
	s, _, _ := actsServer(t, tenantMay(patchClaims, assignLab, readDesktops, patchDesktops), &desktopsSource{})
	wantAct(t, act(s, "stop", "lab-2", "lab-bob", "uid-lab-bob"), http.StatusNoContent, "")
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusNoContent, "")
	rec := do(s, tenantToken, http.MethodGet, "/api/tasks", "")
	wantStatus(t, rec, http.StatusOK)
	got := decodeBody[[]model.TaskEntry](t, rec)
	if len(got) != 2 {
		t.Fatalf("feed = %+v", got)
	}
	slices.SortFunc(got, func(a, b model.TaskEntry) int { return strings.Compare(a.Verb, b.Verb) })
	for i, want := range []struct{ verb, name string }{{"Restart", "solo"}, {"Stop", "lab-2"}} {
		e := got[i]
		if e.Verb != want.verb || e.Name != want.name || !e.Desktop || e.By != "u" || !e.OK || e.Project != "team-a" {
			t.Fatalf("feed = %+v", got)
		}
	}
}

// A restart reads the desktop, then sends one merge patch of its restart
// annotation, both under the caller's token, held to the desktop's UID and to
// that read's resourceVersion: a pool's desktop and one made one by one alike.
// Its value is the time, RFC 3339, within the 64 bytes DeskuBe rolls out.
// Nothing else is read or written: no pod, no Deployment.
func TestRestartAsksDeskuBeAsTheCaller(t *testing.T) {
	s, fc, api := actsServer(t, tenantMay(readDesktops, patchDesktops), &desktopsSource{})
	before := time.Now().UTC().Add(-time.Second)
	wantAct(t, restart(s, "lab-2", "uid-desk-lab-2"), http.StatusNoContent, "")
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusNoContent, "")
	writes := api.seen()
	if len(writes) != 4 || writes[0] != "get lab-2" || writes[2] != "get solo" {
		t.Fatalf("calls = %q, want one read then one patch per restart", writes)
	}
	for i, desk := range []string{"lab-2", "solo"} {
		body, ok := strings.CutPrefix(writes[2*i+1], "restart "+desk+" ")
		var patch struct {
			Metadata struct {
				UID             string            `json:"uid"`
				ResourceVersion string            `json:"resourceVersion"`
				Annotations     map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if !ok || json.Unmarshal([]byte(body), &patch) != nil {
			t.Fatalf("write = %q", writes[2*i+1])
		}
		asked := patch.Metadata.Annotations["vdi.desku.be/restart"]
		at, err := time.Parse(time.RFC3339, asked)
		if patch.Metadata.UID != "uid-desk-"+desk || patch.Metadata.ResourceVersion != "7" || len(patch.Metadata.Annotations) != 1 ||
			err != nil || len(asked) > 64 || at.Location() != time.UTC || at.Before(before) || at.After(time.Now().Add(time.Second)) {
			t.Fatalf("restart of %s patched %s", desk, body)
		}
		if strings.Contains(body, `"spec"`) || strings.Contains(body, `"status"`) {
			t.Fatalf("restart of %s wrote more than its request: %s", desk, body)
		}
	}
	for _, call := range fc.seen() {
		if strings.HasSuffix(call, "/selfsubjectaccessreviews") {
			continue
		}
		if p := strings.TrimPrefix(strings.TrimPrefix(call, "GET "), "PATCH "); p != desktopsPath+"lab-2" && p != desktopsPath+"solo" {
			t.Fatalf("a restart also sent %s", call)
		}
	}
}

// What a restart takes: get and patch on that desktop, granted on every one
// or on it by name. Each refusal names it and reads nothing.
func TestRestartTakesGetAndPatchOnTheDesktop(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules []rule
		code  int
		says  string
	}{
		{"get and patch on desktops", []rule{readDesktops, patchDesktops}, http.StatusNoContent, ""},
		{"granted by name", []rule{{"get", "vdiinstances", "lab-2"}, {"patch", "vdiinstances", "lab-2"}}, http.StatusNoContent, ""},
		{"patch alone", []rule{patchDesktops}, http.StatusForbidden, "you may not restart lab-2: that takes get and patch on vdiinstances lab-2 in team-a-dev"},
		{"on another desktop", []rule{readDesktops, {"patch", "vdiinstances", "lab-1"}}, http.StatusForbidden, "you may not restart lab-2"},
		{"the claim's rights are no desktop's", []rule{patchClaims, assignLab}, http.StatusForbidden, "you may not restart lab-2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, api := actsServer(t, tenantMay(c.rules...), &desktopsSource{})
			wantAct(t, restart(s, "lab-2", "uid-desk-lab-2"), c.code, c.says)
			if c.code != http.StatusNoContent && len(api.seen()) > 0 {
				t.Fatalf("a refused restart called %q", api.seen())
			}
		})
	}
}

// A restart names the desktop the caller saw, by its UID, and only one whose
// phase shows a pod and whose class exists: a stopped desktop is refused,
// never started, and one waiting for its class would restart when it comes.
func TestRestartAddressesOnlyARunningDesktopTheCallerSaw(t *testing.T) {
	src := &desktopsSource{}
	s, _, api := actsServer(t, tenantMay(readDesktops, patchDesktops), src)
	wantAct(t, restart(s, "lab-2", ""), http.StatusBadRequest, "uid")
	wantAct(t, restart(s, "lab-2", "uid-desk-earlier"), http.StatusConflict, "desktop lab-2 is not the one you saw")
	wantAct(t, restart(s, "gone", "uid-desk-gone"), http.StatusNotFound, "desktop team-a-dev/gone")
	for _, phase := range []string{"Stopped", "Stopping", "Provisioning", "Failed"} {
		src.write(t, s, watch.Modified, deskInstance("team-a-dev", "solo", phase))
		wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict, "solo has no pod to restart: it is "+strings.ToLower(phase))
	}
	noClass := deskInstance("team-a-dev", "solo", "Ready")
	noClass.Object["status"] = map[string]any{"phase": "Ready", "conditions": []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "ClassNotFound"},
	}}
	src.write(t, s, watch.Modified, noClass)
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict,
		"class workstation of solo does not exist: DeskuBe rolls out no restart until it does")
	if len(api.seen()) > 0 {
		t.Fatalf("a refused restart called %q", api.seen())
	}
	src.write(t, s, watch.Modified, deskInstance("team-a-dev", "solo", "Starting"))
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusNoContent, "")
}

// A restart ends its owner's session: it lands only on the desktop still for
// the person the caller saw. A pool gives a warm desktop to bob in place,
// under the same UID; a declared desktop's owner changes in git. Either is
// refused whether the snapshot heard of it, or only the desktop read before
// the patch says so.
func TestARestartLandsOnlyOnThePersonSeen(t *testing.T) {
	src := &desktopsSource{}
	s, fc, api := actsServer(t, tenantMay(readDesktops, patchDesktops), src)
	api.mu.Lock()
	api.desks["lab-w"], api.owners["lab-w"], api.rvs["lab-w"] = "uid-desk-lab-w", "", 3
	api.mu.Unlock()
	api.serveDesktop(fc, "lab-w")
	src.write(t, s, watch.Added, owned(heldDesktop("team-a-dev", "lab-w", ""), ""))
	patches := func() (n int) {
		for _, w := range api.seen() {
			if strings.HasPrefix(w, "restart ") {
				n++
			}
		}
		return n
	}

	// bob claims the warm desktop; the snapshot has not heard yet.
	api.mu.Lock()
	api.owners["lab-w"] = "bob"
	api.mu.Unlock()
	wantAct(t, restartFor(s, "lab-w", "uid-desk-lab-w", ""), http.StatusConflict, "desktop lab-w changed hands since you saw it")
	// Now it has: refused before any read.
	src.write(t, s, watch.Modified, owned(heldDesktop("team-a-dev", "lab-w", "uid-lab-bob"), "bob"))
	reads := len(api.seen())
	wantAct(t, restartFor(s, "lab-w", "uid-desk-lab-w", ""), http.StatusConflict, "desktop lab-w changed hands since you saw it")
	if len(api.seen()) != reads {
		t.Fatalf("a restart the snapshot refuses read the desktop: %q", api.seen()[reads:])
	}

	// The declared solo is carol's now in git; the snapshot lags, then hears.
	api.mu.Lock()
	api.owners["solo"] = "carol"
	api.mu.Unlock()
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict, "desktop solo changed hands since you saw it")
	src.write(t, s, watch.Modified, owned(deskInstance("team-a-dev", "solo", "Ready"), "carol"))
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict, "desktop solo changed hands since you saw it")
	if n := patches(); n != 0 {
		t.Fatalf("%d patches sent on a desktop that changed hands", n)
	}

	// Restarted as the person it is for now, it goes.
	wantAct(t, restartFor(s, "lab-w", "uid-desk-lab-w", "bob"), http.StatusNoContent, "")
	wantAct(t, restartFor(s, "solo", "uid-desk-solo", "carol"), http.StatusNoContent, "")
}

// A write lands on the desktop between the read and the patch: the patch,
// held to the read's resourceVersion, is refused, and the caller reads why.
func TestARestartRacingAWriteIsRefused(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(readDesktops, patchDesktops), &desktopsSource{})
	api.mu.Lock()
	api.movesOnRead["solo"] = true
	api.mu.Unlock()
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict, "desktop solo changed since you saw it: look at it again")
}

// The desktop was deleted and another made under its name, which the snapshot
// has not heard of yet: the read says so, and nothing is patched.
func TestADesktopMadeAgainIsNeverRestarted(t *testing.T) {
	s, _, api := actsServer(t, tenantMay(readDesktops, patchDesktops), &desktopsSource{})
	api.mu.Lock()
	api.desks["solo"] = "uid-desk-solo-anew"
	api.mu.Unlock()
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusConflict,
		"desktop solo is not the one you saw: it was deleted, and another made under its name")
	if got := api.seen(); !slices.Equal(got, []string{"get solo"}) {
		t.Fatalf("calls = %q, want the read alone", got)
	}
	if ops := s.tasks.Ops(); len(ops) != 1 || ops[0].OK || ops[0].Verb != "Restart" {
		t.Fatalf("feed = %+v, want the failed restart", ops)
	}
}

// A restart reads the desktops alone: it waits while their watch is down,
// never on the claims'.
func TestRestartWaitsOnTheDesktopsWatchAlone(t *testing.T) {
	s, _, _ := actsServer(t, tenantMay(readDesktops, patchDesktops), &desktopsSource{failClaims: true})
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusNoContent, "")

	src := &desktopsSource{}
	s, _, api := actsServer(t, tenantMay(readDesktops, patchDesktops), src)
	src.dropDesktops(t, s)
	wantAct(t, restart(s, "solo", "uid-desk-solo"), http.StatusServiceUnavailable, "the console's watch on the desktops is down")
	if len(api.seen()) > 0 {
		t.Fatalf("a restart on a stale snapshot called %q", api.seen())
	}
}

func desktopCaps(t *testing.T, s *Server, query string) *model.DesktopCaps {
	t.Helper()
	rec := do(s, tenantToken, http.MethodGet, "/api/capabilities?"+query, "")
	wantStatus(t, rec, http.StatusOK)
	return decodeBody[model.Capabilities](t, rec).Desktops
}

// The capability fields ask what the routes ask, the reads included, of the
// desktop and the claim holding it by name, and answer only for a desktop
// the caller may see and the claim holding it.
func TestDesktopCapsMirrorTheActs(t *testing.T) {
	restartRights := []rule{readDesktops, patchDesktops}
	for _, c := range []struct {
		name    string
		rules   []rule
		desktop string
		claim   string
		want    model.DesktopCaps
	}{
		{"everything on another's", append([]rule{patchClaims, deleteClaims, assignLab}, restartRights...), "lab-2", "lab-bob",
			model.DesktopCaps{Claim: "lab-bob", Restart: true, Power: true, Release: true}},
		{"a pool's user on another's", []rule{patchClaims, deleteClaims, useLab}, "lab-2", "lab-bob",
			model.DesktopCaps{Claim: "lab-bob"}},
		{"a pool's user on its own", []rule{patchClaims, deleteClaims, useLab}, "lab-1", "lab-u",
			model.DesktopCaps{Claim: "lab-u", Power: true, Release: true}},
		{"the pool's deleter", []rule{patchClaims, deleteClaims, deleteLab}, "lab-2", "lab-bob",
			model.DesktopCaps{Claim: "lab-bob", Power: true, Release: true}},
		{"claims alone, its own", []rule{patchClaims, deleteClaims}, "lab-1", "lab-u",
			model.DesktopCaps{Claim: "lab-u", Release: true}},
		{"the claim verbs granted by name", []rule{{"patch", "vdiclaims", "lab-bob"}, {"delete", "vdiclaims", "lab-bob"}, assignLab}, "lab-2", "lab-bob",
			model.DesktopCaps{Claim: "lab-bob", Power: true, Release: true}},
		{"the claim verbs granted on another claim", []rule{{"patch", "vdiclaims", "lab-u"}, assignLab}, "lab-2", "lab-bob",
			model.DesktopCaps{Claim: "lab-bob"}},
		{"restart granted by name", []rule{{"get", "vdiinstances", "lab-2"}, {"patch", "vdiinstances", "lab-2"}}, "lab-2", "",
			model.DesktopCaps{Restart: true}},
		{"restart without get", []rule{patchDesktops}, "lab-2", "",
			model.DesktopCaps{}},
		{"restart on another desktop", []rule{readDesktops, {"patch", "vdiinstances", "lab-1"}}, "lab-2", "",
			model.DesktopCaps{}},
		{"one made one by one", append([]rule{patchClaims, deleteClaims, assignLab}, restartRights...), "solo", "",
			model.DesktopCaps{Restart: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := actsServer(t, tenantMay(c.rules...), &desktopsSource{})
			got := desktopCaps(t, s, "desktops=team-a-dev&desktop="+c.desktop+"&claim="+c.claim)
			c.want.Namespace, c.want.Desktop = "team-a-dev", c.desktop
			if got == nil || *got != c.want {
				t.Fatalf("caps = %+v, want %+v", got, c.want)
			}
		})
	}
	s, _, _ := actsServer(t, tenantMay(append([]rule{patchClaims, deleteClaims, assignLab}, restartRights...)...), &desktopsSource{})
	// A namespace whose desktops the caller may not list offers nothing.
	if got := desktopCaps(t, s, "desktops=team-b-dev&desktop=lab-2&claim=lab-bob"); *got != (model.DesktopCaps{Namespace: "team-b-dev", Desktop: "lab-2"}) {
		t.Fatalf("caps where nothing is listed = %+v", got)
	}
	// A desktop not there, a claim not holding it (one gone, one holding
	// another desktop of its pool, one on a desktop made one by one), and no
	// desktop at all, are not answered.
	for query, code := range map[string]int{
		"desktops=team-a-dev&desktop=gone":               http.StatusNotFound,
		"desktops=team-a-dev&desktop=lab-2&claim=gone":   http.StatusNotFound,
		"desktops=team-a-dev&desktop=lab-2&claim=lab-u":  http.StatusNotFound,
		"desktops=team-a-dev&desktop=solo&claim=lab-bob": http.StatusNotFound,
		"desktops=team-a-dev":                            http.StatusBadRequest,
	} {
		wantStatus(t, do(s, tenantToken, http.MethodGet, "/api/capabilities?"+query, ""), code)
	}
}
