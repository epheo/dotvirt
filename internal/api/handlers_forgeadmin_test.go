package api

import (
	"bytes"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/model"
)

const forgeAdminPassword = "s3cret-forge-admin"

var forgeAdminRef = config.ForgeAdminRef{Namespace: "dotvirt", Name: "dotvirt-forgejo-admin", Key: "password", User: "dotvirt-bot"}

// forgeAdminServer serves the admin Secret to admin-token alone and records
// the bearer of every read of it.
func forgeAdminServer(t *testing.T, ref config.ForgeAdminRef) (*Server, func() []string) {
	t.Helper()
	fc := newFakeCluster(t)
	fc.ssar = func(token string, a *authzv1.ResourceAttributes) bool {
		return token == adminToken && a.Verb == verbGet && a.Resource == "secrets" &&
			a.Namespace == forgeAdminRef.Namespace && a.Name == forgeAdminRef.Name
	}
	var mu sync.Mutex
	var bearers []string
	fc.on(http.MethodGet, "/api/v1/namespaces/dotvirt/secrets/dotvirt-forgejo-admin", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bearers = append(bearers, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+adminToken {
			writeStatus(w, http.StatusForbidden, metav1.StatusReasonForbidden)
			return
		}
		writeObj(w, corev1.Secret{
			TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: "dotvirt-forgejo-admin", Namespace: "dotvirt"},
			Data:       map[string][]byte{"password": []byte(forgeAdminPassword)},
		})
	})
	s := fc.server(nil, Config{ForgeAdmin: ref})
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(bearers)
	}
}

// The password is read under the caller's token on their request only: the
// capability asks a review and reads nothing, the answer is never cached, and
// nothing logs it.
func TestForgeAdminPasswordIsTheCallersRead(t *testing.T) {
	logs := &lockedBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	s, bearers := forgeAdminServer(t, forgeAdminRef)

	caps := func(token string) bool {
		c := decodeBody[model.Capabilities](t, do(s, token, http.MethodGet, "/api/capabilities", "")).Cluster
		return c != nil && c.ForgeAdmin
	}
	if !caps(adminToken) || caps(tenantToken) {
		t.Errorf("forgeAdmin: admin %v, tenant %v; want true, false", caps(adminToken), caps(tenantToken))
	}
	if got := bearers(); len(got) != 0 {
		t.Fatalf("capabilities read the Secret: %v", got)
	}

	rec := do(s, adminToken, http.MethodPost, "/api/forge/admin-password", "")
	wantStatus(t, rec, http.StatusOK)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if got := decodeBody[model.ForgeAdminCredential](t, rec); got.Username != "dotvirt-bot" || got.Password != forgeAdminPassword {
		t.Errorf("credential = %+v", got)
	}

	rec = do(s, tenantToken, http.MethodPost, "/api/forge/admin-password", "")
	wantStatus(t, rec, http.StatusForbidden)
	if strings.Contains(rec.Body.String(), forgeAdminPassword) {
		t.Errorf("a refusal carries the password: %s", rec.Body.String())
	}

	if got, want := bearers(), []string{"Bearer " + adminToken, "Bearer " + tenantToken}; !slices.Equal(got, want) {
		t.Errorf("Secret reads by %v, want %v (the callers', never dotvirt's)", got, want)
	}
	if strings.Contains(logs.String(), forgeAdminPassword) {
		t.Error("the password reached the log")
	}
}

// lockedBuffer collects the log the background reflectors also write to.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Without a managed forge there is no route and no capability.
func TestNoForgeAdminWithoutAManagedForge(t *testing.T) {
	s, bearers := forgeAdminServer(t, config.ForgeAdminRef{})
	if c := decodeBody[model.Capabilities](t, do(s, adminToken, http.MethodGet, "/api/capabilities", "")).Cluster; c == nil || c.ForgeAdmin {
		t.Errorf("cluster caps = %+v, want forgeAdmin false", c)
	}
	if rec := do(s, adminToken, http.MethodPost, "/api/forge/admin-password", ""); rec.Code == http.StatusOK {
		t.Errorf("POST without a managed forge = %d %s", rec.Code, rec.Body.String())
	}
	if got := bearers(); len(got) != 0 {
		t.Errorf("Secret read without a managed forge: %v", got)
	}
}
