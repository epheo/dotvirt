package forge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApprovalsLatestPerReviewerWins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/dotvirt/team-a/pulls/7/reviews" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{
			// alice approved, then requested changes: her approval is withdrawn.
			{"state": "APPROVED", "user": map[string]any{"login": "alice"}},
			{"state": "REQUEST_CHANGES", "user": map[string]any{"login": "alice"}},
			// bob approved; a later COMMENT must not withdraw it.
			{"state": "APPROVED", "user": map[string]any{"login": "bob"}},
			{"state": "COMMENT", "user": map[string]any{"login": "bob"}},
			// carol's approval was dismissed.
			{"state": "APPROVED", "dismissed": true, "user": map[string]any{"login": "carol"}},
			// system row without a login is ignored.
			{"state": "APPROVED", "user": map[string]any{"login": ""}},
		})
	}))
	defer srv.Close()
	n, err := testClient(srv.URL).Approvals(7)
	if err != nil || n != 1 {
		t.Fatalf("Approvals = (%d, %v), want 1 (bob only)", n, err)
	}
}

func TestRequiredApprovals(t *testing.T) {
	t.Run("rule for base branch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, []map[string]any{
				{"branch_name": "other", "required_approvals": 3},
				{"rule_name": "main", "required_approvals": 1},
			})
		}))
		defer srv.Close()
		n, ok, err := testClient(srv.URL).RequiredApprovals("main")
		if err != nil || !ok || n != 1 {
			t.Fatalf("RequiredApprovals = (%d, %v, %v), want (1, true, nil)", n, ok, err)
		}
	})
	t.Run("no rule", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, []map[string]any{})
		}))
		defer srv.Close()
		if _, ok, err := testClient(srv.URL).RequiredApprovals("main"); ok || err != nil {
			t.Fatalf("want (ok=false, nil) on no rule, got ok=%v err=%v", ok, err)
		}
	})
	t.Run("forbidden is an error, not none", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		if _, _, err := testClient(srv.URL).RequiredApprovals("main"); err == nil {
			t.Fatal("want error on 403 (unknown must not read as no-rule)")
		}
	})
}

func TestCombinedStatus(t *testing.T) {
	t.Run("reported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"state": "success", "total_count": 2})
		}))
		defer srv.Close()
		st, err := testClient(srv.URL).CombinedStatus("abc123")
		if err != nil || st != "success" {
			t.Fatalf("CombinedStatus = (%q, %v), want success", st, err)
		}
	})
	t.Run("no statuses means none, not pending", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"state": "pending", "total_count": 0})
		}))
		defer srv.Close()
		st, err := testClient(srv.URL).CombinedStatus("abc123")
		if err != nil || st != "" {
			t.Fatalf("CombinedStatus = (%q, %v), want empty", st, err)
		}
	})
}
