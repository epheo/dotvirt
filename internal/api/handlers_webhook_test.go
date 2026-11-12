package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/epheo/dotvirt/pkg/forge"
)

func webhookServer(secret string) *Server {
	return NewServer(Deps{Reader: &fakeDraft{}, Draft: &fakeDraft{}, Config: Config{WebhookSecret: secret}})
}

func deliver(t *testing.T, s *Server, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/forge", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("X-Forgejo-Signature", sig)
	}
	w := httptest.NewRecorder()
	s.handleForgeWebhook(w, req)
	return w
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// A correctly signed delivery is accepted; the repo named in the payload may be
// unknown (not yet opened) - that must not error.
func TestWebhookValidSignature(t *testing.T) {
	s := webhookServer("hooksecret")
	body := []byte(`{"repository":{"clone_url":"https://forge/o/r.git","html_url":"https://forge/o/r"}}`)
	if w := deliver(t, s, body, sign(body, "hooksecret")); w.Code != http.StatusNoContent {
		t.Fatalf("valid delivery: got %d, want 204", w.Code)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	s := webhookServer("hooksecret")
	body := []byte(`{}`)
	if w := deliver(t, s, body, sign(body, "wrong")); w.Code != http.StatusForbidden {
		t.Fatalf("forged delivery: got %d, want 403", w.Code)
	}
	if w := deliver(t, s, body, ""); w.Code != http.StatusForbidden {
		t.Fatalf("unsigned delivery: got %d, want 403", w.Code)
	}
}

func TestWebhookDisabledWithoutSecret(t *testing.T) {
	s := webhookServer("")
	if w := deliver(t, s, []byte(`{}`), ""); w.Code != http.StatusNotFound {
		t.Fatalf("unconfigured endpoint: got %d, want 404", w.Code)
	}
}

// A push reaches the proposals lane through the poke's GitChanged, so it must
// not nudge; a PR event can move no head, so it nudges its repo.
func TestWebhookNudgesProposalsOnPREventsOnly(t *testing.T) {
	s := webhookServer("hooksecret")
	push := []byte(`{"repository":{"clone_url":"https://forge/o/r.git","html_url":"https://forge/o/r"}}`)
	deliver(t, s, push, sign(push, "hooksecret"))
	select {
	case <-s.propNudge:
		t.Fatal("a push event nudged the proposals refresher")
	default:
	}

	pr := []byte(`{"action":"closed","pull_request":{"number":3},"repository":{"clone_url":"https://forge/o/r.git","html_url":"https://forge/o/r"}}`)
	deliver(t, s, pr, sign(pr, "hooksecret"))
	select {
	case <-s.propNudge:
	default:
		t.Fatal("a PR event must nudge the proposals refresher")
	}
	s.propMu.Lock()
	_, dirty := s.propDirty["https://forge/o/r"]
	s.propMu.Unlock()
	if !dirty {
		t.Fatalf("the nudge must name the PR's repo, dirty = %v", s.propDirty)
	}
}

// Forgejo names its repos at ROOT_URL, the forge's web base when one is set,
// while dotvirt keys them by the URL it clones from: a delivery must reach that
// repo's lane all the same.
func TestWebhookReadsTheWebBaseAsTheForges(t *testing.T) {
	s := NewServer(Deps{
		Forge:  forge.NewFactory("http://forge.ns.svc:3000", forge.StaticToken("t"), false, "").WithWebURL("https://forge.example"),
		Reader: &fakeDraft{},
		Draft:  &fakeDraft{},
		Config: Config{WebhookSecret: "hooksecret"},
	})
	pr := []byte(`{"action":"closed","pull_request":{"number":3},"repository":{"clone_url":"https://forge.example/o/r.git","html_url":"https://forge.example/o/r"}}`)
	deliver(t, s, pr, sign(pr, "hooksecret"))
	s.propMu.Lock()
	_, dirty := s.propDirty["http://forge.ns.svc:3000/o/r"]
	s.propMu.Unlock()
	if !dirty {
		t.Fatalf("the nudge must name the repo by its clone URL, dirty = %v", s.propDirty)
	}
}
