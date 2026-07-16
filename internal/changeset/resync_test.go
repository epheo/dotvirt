package changeset

import (
	"context"
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
)

type fakeResyncer struct{ called bool }

func (f *fakeResyncer) Resync(ctx context.Context, namespace, name string) (model.ResyncResult, error) {
	f.called = true
	return model.ResyncResult{Application: "app"}, nil
}

func TestResyncEnforcesCallerAuthority(t *testing.T) {
	c := newTestCoordinator(t)
	rs := &fakeResyncer{}
	c.resyncer = rs

	deny := func(context.Context, string, string) (bool, error) { return false, nil }
	if _, err := c.Resync(context.Background(), deny, "alpha", "web"); !errors.Is(err, model.ErrForbidden) {
		t.Fatalf("denied caller: want ErrForbidden, got %v", err)
	}
	if rs.called {
		t.Fatal("resyncer reached despite denied SSAR")
	}

	allow := func(context.Context, string, string) (bool, error) { return true, nil }
	res, err := c.Resync(context.Background(), allow, "alpha", "web")
	if err != nil || res.Application != "app" {
		t.Fatalf("allowed caller: got %+v, %v", res, err)
	}
}

func TestResyncUnavailableWithoutArgo(t *testing.T) {
	c := newTestCoordinator(t)
	allow := func(context.Context, string, string) (bool, error) { return true, nil }
	if _, err := c.Resync(context.Background(), allow, "a", "b"); !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
