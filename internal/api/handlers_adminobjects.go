package api

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/pkg/adminobjects"
)

// adminObjectsOffered is whether this console renders admin objects: the
// operator says it belongs to an install, and it knows its own namespace.
// Every pin comes from reads under the caller's token.
func (s *Server) adminObjectsOffered() bool {
	return s.cfg.Install.Name != "" && s.cfg.OwnNamespace != ""
}

// adminObjectsAction is what installing the admin objects takes: reading the
// install in the console's namespace, finding the operator's Deployment and
// reviewing its account, then reading, creating and patching every object an
// install may ask for. Asked of the allowlist, so the capability reads nothing.
func (s *Server) adminObjectsAction() action {
	ns := s.cfg.OwnNamespace
	act := action{
		{Verb: verbList, Group: "dotvirt.io", Resource: "dotvirts"},
		{Verb: verbList, Group: "apps", Resource: "deployments", Name: adminobjects.OperatorDeployment},
		{Verb: verbGet, Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: adminobjects.ConsoleAccount},
		{Verb: verbCreate, Group: "authorization.k8s.io", Resource: "subjectaccessreviews"},
		{Verb: verbGet, Group: "config.openshift.io", Resource: "ingresses", Name: "cluster"},
		{Verb: verbGet, Group: "route.openshift.io", Resource: "routes", Namespace: ns, Name: adminobjects.ConsoleDeployment},
	}
	for _, r := range adminobjects.Allowed(ns) {
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

// adminParams are the admin objects' pins, every one read as the caller, none
// from anything the operator writes: the console's own namespace (its
// ServiceAccount mount), the one install there (its ArgoCD target, image, TLS
// and metrics choices, and its forge credential, whose grant the operator's
// account gets; the operator may only patch its finalizers once the guard is
// in), and the operator's Deployment (its account and images; OLM or GitOps
// writes it).
func (s *Server) adminParams(ctx context.Context, c *cluster.Client) (adminobjects.Params, error) {
	ns := s.cfg.OwnNamespace
	spec, err := c.InstallIn(ctx, ns)
	if err != nil {
		return adminobjects.Params{}, unavailable(fmt.Errorf("read the install: %w", err))
	}
	op, err := c.OperatorDeployment(ctx, adminobjects.OperatorDeployment, ns)
	if err != nil {
		return adminobjects.Params{}, unavailable(fmt.Errorf("find the operator: %w", err))
	}
	// A console the install moved away from would pin the namespace the
	// operator left, which refuses the install it acts on.
	switch acts, err := c.OperatorInstall(ctx, op); {
	case err != nil:
		return adminobjects.Params{}, unavailable(fmt.Errorf("resolve the operator's install: %w", err))
	case acts != ns:
		return adminobjects.Params{}, fmt.Errorf("%w: the operator acts on the install in namespace %q, not on this console's (%s); install the guard from the console there",
			model.ErrConflict, acts, ns)
	}
	openShift, err := c.ServesRoutes()
	if err != nil {
		return adminobjects.Params{}, unavailable(fmt.Errorf("discover the platform: %w", err))
	}
	p := adminobjects.Params{
		Operator:  "system:serviceaccount:" + op.Namespace + ":" + op.ServiceAccount,
		Namespace: ns, SpecImage: spec.Image, DotvirtImage: op.DotvirtImage, ForgejoImage: op.ForgejoImage,
		InsecureTLS: spec.InsecureTLS,
		Metrics:     adminobjects.MetricsFor(spec.MetricsURL, spec.MetricsTenancyURL, spec.MetricsCA),

		BrandingConfigMap: spec.BrandingConfigMap, MetricsCAConfigMap: spec.MetricsCAConfigMap,
		ForgeSecret: adminobjects.ForgeSecret(spec.ForgeCredentials), ForgeManaged: spec.ForgeManaged,
		OIDCSecret: spec.OIDCSecret, OIDCSecretKey: spec.OIDCSecretKey, OIDCIssuer: spec.OIDCIssuer,
		ForgeDelegated: spec.ForgeDelegated, ForgeURL: spec.ForgeURL, ForgeClientID: spec.ForgeClientID,
		ForgeAuthorizeURL: spec.ForgeAuthorizeURL, ForgeTokenURL: spec.ForgeTokenURL,
		ForgeClientSecret: spec.ForgeClientSecret, ForgeClientSecretKey: spec.ForgeClientSecretKey,
		InstallName: spec.Name, InstallUID: spec.UID,
	}
	p.ArgoNamespace, p.ArgoController = adminobjects.ArgoTarget(spec.ArgoNamespace, spec.ArgoController, openShift)
	p.ConsoleGateway = adminobjects.GatewayOf(spec.GatewayName, spec.GatewayNamespace, spec.GatewaySectionName, ns)
	if p.ForgeGateway = adminobjects.GatewayOf(spec.ForgeGatewayName, spec.ForgeGatewayNamespace, spec.ForgeGatewaySectionName, ns); p.ForgeGateway.Name == "" {
		p.ForgeGateway = p.ConsoleGateway
	}
	onRoute := spec.IngressHost == "" && adminobjects.ExposureType(spec.IngressType, spec.GatewayName != "", openShift) == adminobjects.ExposureRoute
	domain := ""
	if onRoute {
		switch d, err := c.AppsDomain(ctx); {
		case err == nil:
			domain = d
		case !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err):
			return adminobjects.Params{}, unavailable(fmt.Errorf("read the cluster's ingress domain: %w", err))
		}
	}
	router := ""
	if onRoute && domain == "" {
		switch h, err := c.RouterHost(ctx, ns, adminobjects.ConsoleDeployment); {
		case err == nil:
			router = h
		case !apierrors.IsNotFound(err):
			return adminobjects.Params{}, unavailable(fmt.Errorf("read the console's Route: %w", err))
		}
	}
	p.ConsoleHost = adminobjects.ConsoleHost(spec.IngressHost, ns, domain, router)
	if bad := p.Malformed(); bad != "" {
		return adminobjects.Params{}, unavailable(fmt.Errorf("the install's %s is not well formed", bad))
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
			obj := o.Object
			// Over a live object, keep its owner references as they are: adding
			// one takes delete on the object (OwnerReferencesPermissionEnforcement),
			// and a grant an admin made by hand stays theirs.
			live, err := c.AdminObject(ctx, ref)
			if err != nil {
				return model.AdminObjects{}, unavailable(err)
			}
			if live != nil {
				obj = obj.DeepCopy()
				obj.SetOwnerReferences(live.GetOwnerReferences())
			}
			if err := c.ApplyAdminObject(ctx, ref, obj); err != nil {
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
	// What the install no longer asks for but is still there: listed to remove,
	// never applied.
	for _, ref := range adminobjects.Leftovers(p) {
		live, err := c.AdminObject(ctx, ref)
		if err != nil {
			return model.AdminObjects{}, unavailable(err)
		}
		if live != nil {
			out.Objects = append(out.Objects, model.AdminObject{
				Purpose: adminobjects.LeftoverPurpose, Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace,
				State: string(adminobjects.Stale), Detail: adminobjects.LeftoverDetail,
			})
		}
	}
	return out, nil
}
