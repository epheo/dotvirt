package api

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// adminObjectsOffered is whether this console renders admin objects: it knows
// its install and every pin of the guard.
func (s *Server) adminObjectsOffered() bool {
	return s.cfg.Install.Name != "" && s.cfg.AdminPins.Valid()
}

// adminObjectsAction is what installing the admin objects takes: reading the
// install and the operator's Deployment (the images an admin chose), then
// reading, creating and patching every object an install may ask for. Asked
// of the allowlist, so the capability reads nothing.
func (s *Server) adminObjectsAction() action {
	in, pins := s.cfg.Install, s.cfg.AdminPins
	act := action{
		{Verb: verbGet, Group: "dotvirt.io", Resource: "dotvirts", Namespace: in.Namespace, Name: in.Name},
		{Verb: verbGet, Group: "apps", Resource: "deployments", Namespace: pins.OperatorNamespace(), Name: adminobjects.OperatorDeployment},
	}
	for _, r := range adminobjects.Allowed(in.Namespace) {
		act = append(act,
			question{Verb: verbCreate, Group: r.Group, Resource: r.Resource, Namespace: r.Namespace},
			question{Verb: verbPatch, Group: r.Group, Resource: r.Resource, Namespace: r.Namespace, Name: r.Name},
			question{Verb: verbGet, Group: r.Group, Resource: r.Resource, Namespace: r.Namespace, Name: r.Name})
	}
	return act
}

// handleAdminObjects lists the admin objects, as this console renders them,
// with their state, read under the caller's token.
func (s *Server) handleAdminObjects(w http.ResponseWriter, r *http.Request) {
	s.serveAdminObjects(w, r, false)
}

// handleInstallAdminObjects applies the console's own render of the admin
// objects under the caller's token: the apiserver's RBAC is the gate, and
// nothing the operator writes chooses what is applied.
func (s *Server) handleInstallAdminObjects(w http.ResponseWriter, r *http.Request) {
	s.serveAdminObjects(w, r, true)
}

func (s *Server) serveAdminObjects(w http.ResponseWriter, r *http.Request, install bool) {
	id, c, err := s.userCluster(r)
	if err != nil {
		fail(w, unavailable(err))
		return
	}
	out, err := s.adminObjects(r.Context(), id, c, install)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, out)
}

// adminParams completes the console's pins with the images an admin chose,
// read as the caller: the install's spec.image and the operator Deployment's
// RELATED_IMAGE_* env, which OLM or GitOps writes and the operator cannot.
func (s *Server) adminParams(ctx context.Context, c *cluster.Client) (adminobjects.Params, error) {
	p, in := s.cfg.AdminPins, s.cfg.Install
	var err error
	if p.SpecImage, err = c.InstallImage(ctx, in.Namespace, in.Name); err != nil {
		return p, unavailable(fmt.Errorf("read the install %s/%s: %w", in.Namespace, in.Name, err))
	}
	if p.DotvirtImage, p.ForgejoImage, err = c.OperatorImages(ctx, p.OperatorNamespace(), adminobjects.OperatorDeployment); err != nil {
		return p, unavailable(fmt.Errorf("read the operator Deployment %s/%s: %w", p.OperatorNamespace(), adminobjects.OperatorDeployment, err))
	}
	return p, nil
}

// adminObjects renders the admin objects, applies them when install, and
// answers each one's state against the live object.
func (s *Server) adminObjects(ctx context.Context, id auth.Identity, c *cluster.Client, install bool) (model.AdminObjects, error) {
	if !s.capable(ctx, id, c, s.adminObjectsAction()) {
		return model.AdminObjects{}, fmt.Errorf("%w: installing what the install needs takes a cluster admin", model.ErrForbidden)
	}
	p, err := s.adminParams(ctx, c)
	if err != nil {
		return model.AdminObjects{}, err
	}
	out := model.AdminObjects{
		Operator: p.Operator, Namespace: p.Namespace, ArgoNamespace: p.ArgoNamespace, ArgoController: p.ArgoController,
		Images: []string{}, Objects: []model.AdminObject{},
	}
	for _, img := range []string{p.SpecImage, p.DotvirtImage, p.ForgejoImage} {
		if img != "" {
			out.Images = append(out.Images, img)
		}
	}
	allowed := adminobjects.Allowed(p.Namespace)
	for _, o := range adminobjects.Render(p) {
		ref, ok := adminobjects.Allow(allowed, o.Object)
		if !ok {
			return model.AdminObjects{}, fmt.Errorf("the console renders %s %s, which no install may ask for", o.Object.GetKind(), o.Object.GetName())
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
