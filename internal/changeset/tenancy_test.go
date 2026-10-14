package changeset

import (
	"errors"
	"testing"

	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/project"
)

// A tenant named after the platform tier would share its draft and gates.
func TestTenantNameRefusesReserved(t *testing.T) {
	if err := tenantName(project.PlatformName); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("tenantName(%q) = %v, want ErrInvalid", project.PlatformName, err)
	}
	if err := tenantName("team-a"); err != nil {
		t.Fatalf("tenantName(team-a) = %v", err)
	}
}
