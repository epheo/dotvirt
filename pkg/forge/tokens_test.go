package forge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 401 from the admin token API (basic-auth rejected) must surface as ErrUnauthorized
// so the operator can distinguish an admin-password mismatch from a forge outage. The
// mismatch can arrive at either leg: deleteToken (MintToken clears a duplicate name
// first) or the token POST.
func TestMintTokenUnauthorizedMapsToErrUnauthorized(t *testing.T) {
	cases := []struct {
		name        string
		delStatus   int
		postStatus  int
		wantUnauth  bool
		wantSuccess bool
	}{
		{"delete 401", http.StatusUnauthorized, http.StatusCreated, true, false},
		{"post 401", http.StatusNotFound, http.StatusUnauthorized, true, false},
		{"success", http.StatusNotFound, http.StatusCreated, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(c.delStatus)
					return
				}
				w.WriteHeader(c.postStatus)
				if c.postStatus == http.StatusCreated {
					_, _ = w.Write([]byte(`{"sha1":"deadbeef"}`))
				}
			}))
			defer srv.Close()

			tok, err := NewFactory(srv.URL, "unused", false).
				MintToken("dotvirt-bot", "pw", "dotvirt-operator", []string{"read:user"})
			if c.wantUnauth && !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v, want ErrUnauthorized", err)
			}
			if c.wantSuccess {
				if err != nil {
					t.Fatalf("MintToken: %v", err)
				}
				if tok != "deadbeef" {
					t.Fatalf("token = %q, want deadbeef", tok)
				}
			}
		})
	}
}
