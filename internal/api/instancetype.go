package api

import (
	"context"
	"errors"
	"time"

	"github.com/epheo/dotvirt/internal/auth"
)

// instancetypeTimeout bounds the one read an instancetype switch makes:
// staging strips every field the instancetype may take over rather than wait.
const instancetypeTimeout = 5 * time.Second

// Instancetype implements changeset.InstancetypeReader: one GET under id's
// own token, so the read is the caller's to make.
func (s *Server) Instancetype(id auth.Identity, namespace, kind, name string) (map[string]any, error) {
	if s.clusterF == nil {
		return nil, unavailable(errors.New("cluster not configured"))
	}
	c, err := s.clusterF.For(id.Token)
	if err != nil {
		return nil, unavailable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), instancetypeTimeout)
	defer cancel()
	return c.Instancetype(ctx, namespace, kind, name)
}
