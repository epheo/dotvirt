package api

import (
	"net/http"

	"github.com/epheo/dotvirt/internal/draft"
)

// routes mounts every /api route on mux; Handler wraps it in auth and the
// transport middleware.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})

	if s.auth != nil {
		mux.HandleFunc("POST /api/login", s.auth.Login)
		mux.HandleFunc("POST /api/logout", s.auth.Logout)
		mux.HandleFunc("GET /api/me", s.auth.Me)
		// The login screen asks which sign-in paths exist before any session.
		// ssoPending says SSO is configured but its OAuthClient isn't registered yet,
		// so the screen can say "finish setup" instead of offering a button that fails.
		mux.HandleFunc("GET /api/auth/methods", func(w http.ResponseWriter, r *http.Request) {
			pending := s.oauth != nil && !s.oauth.ClientRegistered(r.Context())
			writeJSON(w, map[string]bool{"sso": s.oauth != nil, "ssoPending": pending})
		})
		if s.oauth != nil {
			mux.HandleFunc("GET /api/auth/openshift", s.oauth.LoginRedirect)
			mux.HandleFunc("GET /api/auth/callback", s.oauth.Callback)
			mux.HandleFunc("POST /api/auth/oauthclient", s.handleFinishSSO)
		}
	}

	mux.HandleFunc("GET /api/inventory", s.handleInventory)
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("GET /api/options", s.handleOptions)
	mux.HandleFunc("GET /api/storage/classes", s.handleStorageClasses)
	mux.HandleFunc("GET /api/networks", s.handleNetworks)
	mux.HandleFunc("GET /api/policies", s.handlePolicies)
	mux.HandleFunc("POST /api/networks", s.handleCreateNetwork)
	// The single-scope network-family creates, by tier (see handlers_networks.go):
	// an Uplink (nmstate NNCP) is always cluster-scoped, so always the platform
	// tier, gated on the caller's authority to create NNCPs.
	mux.HandleFunc("POST /api/uplinks", s.platformCreate(ssarUplink, draft.ResourceUplink))
	// An egress firewall (the Tier-1 gateway firewall) is namespace-scoped, so it
	// routes to the tenant project owning the namespace, the same path as a
	// project-scoped UDN; the tenant's Argo app applies it on merge (its AppProject
	// must permit k8s.ovn.org/EgressFirewall).
	mux.HandleFunc("POST /api/egressfirewalls", s.namespacedCreate("an egress firewall", draft.ResourceEgressFirewall))
	// An EgressIP (the Tier-0 source-NAT pool) is always platform-tier, gated on
	// the caller's authority to create EgressIPs.
	mux.HandleFunc("POST /api/egressips", s.platformCreate(ssarEgressIP, draft.ResourceEgressIP))
	// An AdminPolicyBasedExternalRoute (the Tier-0 external next-hop route) is
	// always platform-tier, gated on the caller's authority to create them.
	mux.HandleFunc("POST /api/externalroutes", s.platformCreate(ssarExtRoute, draft.ResourceExternalRoute))
	// A NetworkPolicy (the east-west Distributed Firewall) is namespace-scoped, so
	// it routes to the tenant project owning the namespace; the tenant's Argo app
	// applies it on merge.
	mux.HandleFunc("POST /api/networkpolicies", s.namespacedCreate("a network policy", draft.ResourceNetworkPolicy))
	mux.HandleFunc("POST /api/adminnetworkpolicies", s.handleCreateAdminNetworkPolicy)
	// Every create above stages an EDIT when git already declares the object, so
	// the same form and route change one. Read-back for that form, and delete,
	// address the object by its draft identity (see handlers_objects.go).
	mux.HandleFunc("GET /api/objects/{resource}/{namespace}/{name}", s.handleObjectSpec)
	mux.HandleFunc("DELETE /api/objects/{resource}/{namespace}/{name}", s.handleObjectDelete)
	mux.HandleFunc("PUT /api/objects/{resource}/{namespace}/{name}", s.handleObjectUpdateManifest)
	// Adoption for the rest of the inventory: one object by its draft identity,
	// or every cluster-scoped object the platform repo does not yet describe -
	// the namespace adoption's capture and staging, scoped differently.
	mux.HandleFunc("POST /api/objects/{resource}/{namespace}/{name}/adopt", s.handleObjectAdopt)
	// The VM page's history and restore, for every other declared object.
	mux.HandleFunc("GET /api/objects/{resource}/{namespace}/{name}/history", s.handleObjectHistory)
	mux.HandleFunc("POST /api/objects/{resource}/{namespace}/{name}/restore", s.handleObjectRestore)
	mux.HandleFunc("POST /api/platform/adopt", s.handlePlatformAdopt)
	mux.HandleFunc("POST /api/namespaces", s.handleCreateNamespace)
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/templates", s.handleTemplates)
	mux.HandleFunc("POST /api/templates", s.handleSaveTemplate)
	mux.HandleFunc("PUT /api/templates", s.handleUpdateTemplate)
	mux.HandleFunc("POST /api/templates/deploy", s.handleDeployTemplate)
	mux.HandleFunc("GET /api/drs", s.handleDRS)
	mux.HandleFunc("POST /api/drs", s.handleDRSEnable)
	mux.HandleFunc("DELETE /api/drs", s.handleDRSDisable)
	// ArgoCD ApplicationSet plugin generator (auth: its own shared token, not a
	// user session - exempted in auth.isOpenPath). Emits projects from labels.
	mux.HandleFunc("POST /api/v1/getparams.execute", s.handleAppSetPlugin)
	// Forgejo webhook (auth: HMAC delivery signature, not a user session -
	// exempted in auth.isOpenPath). Pokes the repo's poller for instant updates.
	mux.HandleFunc("POST /api/webhooks/forge", s.handleForgeWebhook)
	mux.HandleFunc("GET /api/events", s.handleAllEvents)
	mux.HandleFunc("GET /api/permissions", s.handlePermissions)
	mux.HandleFunc("GET /api/metrics/cluster", s.handleClusterSummary)
	mux.HandleFunc("GET /api/metrics/scope", s.handleScopeMetrics)
	mux.HandleFunc("GET /api/metrics/hosts", s.handleHostLoad)
	mux.HandleFunc("GET /api/metrics/capacity", s.handleHostCapacity)
	mux.HandleFunc("GET /api/alarms", s.handleAlarms)
	mux.HandleFunc("GET /api/quotas", s.handleQuotas)
	mux.HandleFunc("GET /api/nodes", s.handleNodes)
	mux.HandleFunc("GET /api/nodes/{node}", s.handleNodeInfo)
	mux.HandleFunc("POST /api/nodes/{node}/cordon", s.handleNodeCordon)
	mux.HandleFunc("POST /api/nodes/{node}/maintenance", s.handleNodeMaintenance)
	mux.HandleFunc("POST /api/nodes/{node}/evacuate", s.handleNodeEvacuate)
	mux.HandleFunc("POST /api/uploads", s.handleCreateUpload)
	mux.HandleFunc("GET /api/uploads/{namespace}/{name}", s.handleUploadStatus)
	mux.HandleFunc("POST /api/uploads/{namespace}/{name}/token", s.handleUploadToken)
	mux.HandleFunc("GET /api/projects/{project}/history", s.handleHistory)
	mux.HandleFunc("GET /api/projects/{project}/history/{hash}", s.handleCommit)
	mux.HandleFunc("GET /api/projects/{project}/proposals/{number}", s.handleProposal)
	mux.HandleFunc("POST /api/projects/{project}/revert", s.handleRevert)
	mux.HandleFunc("POST /api/projects/{project}/adopt", s.handleAdoptProject)
	mux.HandleFunc("POST /api/projects/{project}/release", s.handleReleaseProject)

	if s.stream != nil {
		mux.HandleFunc("GET /api/inventory/stream", s.stream.Handler)
	}
	if s.vnc != nil {
		mux.HandleFunc("GET /api/vms/{namespace}/{name}/vnc", s.vnc.Handler)
	}

	// Draft changeset routes (all project-scoped).
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/edit", s.handleEdit)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/delete", s.handleDelete)
	mux.HandleFunc("POST /api/vms", s.handleCreate)
	mux.HandleFunc("GET /api/draft", s.handleDraftGet)
	mux.HandleFunc("DELETE /api/draft", s.handleDraftDiscard)
	mux.HandleFunc("DELETE /api/draft/{namespace}/{name}", s.handleUnstage)
	mux.HandleFunc("POST /api/draft/propose", s.handlePropose)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/drift", s.handleDrift)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/policy", s.handleVMPolicy)
	mux.HandleFunc("GET /api/namespaces/{namespace}/policy", s.handleNamespacePolicy)
	mux.HandleFunc("POST /api/networking/trace", s.handleTrace)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/manifest", s.handleManifest)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/history", s.handleVMHistory)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/restore", s.handleRestore)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/events", s.handleEvents)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/screenshot", s.handleScreenshot)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/usage", s.handleVMUsage)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/adopt", s.handleAdopt)
	mux.HandleFunc("POST /api/namespaces/{namespace}/adopt", s.handleAdoptNamespace)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/resync", s.handleResync)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/migrate", s.handleMigrate)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/pause", s.handlePause)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/unpause", s.handleUnpause)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/snapshots", s.handleSnapshots)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/snapshots", s.handleTakeSnapshot)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/snapshots/{snapshot}/restore", s.handleRestoreSnapshot)
	mux.HandleFunc("DELETE /api/vms/{namespace}/{name}/snapshots/{snapshot}", s.handleDeleteSnapshot)
	mux.HandleFunc("GET /api/vms/{namespace}/{name}/clones", s.handleClones)
	mux.HandleFunc("POST /api/vms/{namespace}/{name}/clone", s.handleCreateClone)
}
