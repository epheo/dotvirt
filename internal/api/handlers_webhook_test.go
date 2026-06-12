package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func webhookServer(secret string) *Server {
	return NewServer(Deps{Config: Config{WebhookSecret: secret}})
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
// unknown (not yet opened) — that must not error.
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
