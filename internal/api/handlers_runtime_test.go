package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestRuntimeFailRedactsInternal pins the redaction split for cluster operations:
// the apiserver's own verdict on the caller's object is echoed, but an
// unclassified failure (a dial error naming the apiserver host) is not.
func TestRuntimeFailRedactsInternal(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachineinstances"}, "web-1")
	rec := httptest.NewRecorder()
	runtimeFail(rec, notFound)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "web-1") {
		t.Errorf("classified error: status %d body %q, want 404 echoing the apiserver message", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	runtimeFail(rec, errors.New("dial tcp 10.0.0.1:6443: connect: connection refused"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "10.0.0.1") || !strings.Contains(body, "internal error") {
		t.Errorf("internal detail leaked: %q", body)
	}
}
