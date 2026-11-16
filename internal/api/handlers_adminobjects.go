package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// adminObjectsAction is what installing the install's admin objects takes:
// reading the install, then creating and patching every object it may ask
// for. Asked of the allowlist, not of what the status lists now, so the
// capability reads nothing and covers any list the operator renders.
func (s *Server) adminObjectsAction() action {
	in := s.cfg.Install
	act := action{{Verb: verbGet, Group: "dotvirt.io", Resource: "dotvirts", Namespace: in.Namespace, Name: in.Name}}
	for _, r := range adminobjects.Allowed(in.Namespace) {
		act = append(act,
			question{Verb: verbCreate, Group: r.Group, Resource: r.Resource, Namespace: r.Namespace},
			question{Verb: verbPatch, Group: r.Group, Resource: r.Resource, Namespace: r.Namespace, Name: r.Name})
	}
	return act
}

// handleAdminObjects lists the install's admin objects with their state, read
// under the caller's token.
func (s *Server) handleAdminObjects(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	out, err := s.adminObjects(r.Context(), id, c, false)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// handleInstallAdminObjects applies the install's admin objects under the
// caller's token: the apiserver's RBAC is the gate, so neither dotvirt's
// ServiceAccount nor the operator holds a right to them. Only what
// adminobjects.Allowed names is applied, whatever the status lists.
func (s *Server) handleInstallAdminObjects(w http.ResponseWriter, r *http.Request) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	out, err := s.adminObjects(r.Context(), id, c, true)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// adminObjects reads the install's admin objects as the caller, applies them
// when install, and answers each one's state.
func (s *Server) adminObjects(ctx context.Context, id auth.Identity, c *cluster.Client, install bool) (model.AdminObjects, error) {
	if !s.capable(ctx, id, c, s.adminObjectsAction()) {
		return model.AdminObjects{}, fmt.Errorf("%w: installing what the install needs takes a cluster admin", model.ErrForbidden)
	}
	in := s.cfg.Install
	listed, err := c.InstallAdminObjects(ctx, in.Namespace, in.Name)
	if err != nil {
		return model.AdminObjects{}, unavailable(err)
	}
	allowed := adminobjects.Allowed(in.Namespace)
	out := model.AdminObjects{Objects: []model.AdminObject{}}
	for _, o := range listed {
		ref, ok := adminobjects.Allow(allowed, o.Object)
		if !ok {
			log.Printf("api: admin objects: %s/%s %s %s/%s is not one an install may ask for; skipped",
				in.Namespace, in.Name, o.Object.GetKind(), o.Object.GetNamespace(), o.Object.GetName())
			continue
		}
		if install {
			if err := c.ApplyAdminObject(ctx, ref, o.Object); err != nil {
				if apierrors.IsForbidden(err) {
					return model.AdminObjects{}, fmt.Errorf("%w: you may not apply %s %s", model.ErrForbidden, ref.Kind, ref.Name)
				}
				return model.AdminObjects{}, err
			}
		}
		live, err := c.AdminObject(ctx, ref)
		if err != nil {
			return model.AdminObjects{}, unavailable(err)
		}
		state, detail := adminobjects.Compare(o.Object, live)
		out.Objects = append(out.Objects, model.AdminObject{
			Purpose: o.Purpose, Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace,
			State: string(state), Detail: detail, Object: o.Object.Object,
		})
	}
	return out, nil
}
