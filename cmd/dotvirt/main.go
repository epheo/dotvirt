// Command dotvirt serves a web console, familiar to virtualization admins, that edits
// per-project git repos of KubeVirt manifests and reads live state from a cluster
// and ArgoCD. It is a thin multi-tenant lens: every request runs under the caller's
// own k8s token, and a project is a set of namespaces (a cluster fact) backed by its
// own git repo.
package main

import (
	"cmp"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/epheo/dotvirt/internal/api"
	"github.com/epheo/dotvirt/internal/argo"
	"github.com/epheo/dotvirt/internal/auth"
	"github.com/epheo/dotvirt/internal/branding"
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/desktops"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/drs"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/model"
	"github.com/epheo/dotvirt/internal/modules"
	"github.com/epheo/dotvirt/internal/netstate"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/publish"
	"github.com/epheo/dotvirt/internal/replication"
	"github.com/epheo/dotvirt/internal/routes"
	"github.com/epheo/dotvirt/internal/stream"
	"github.com/epheo/dotvirt/internal/tasks"
	"github.com/epheo/dotvirt/pkg/forge"
)

// liveVMs adapts the SA snapshot to the coordinator's actual-state source,
// serialized with the repo's own writer so a diff sees only real drift. One
// unserializable VM drops rather than failing the batch.
type liveVMs struct{ state *clusterstate.State }

// Ready gates on the stores this reads (VMs + namespaces), not full Synced(): a
// permanently-failing VMI reflector must not wedge drift and adoption forever.
func (l liveVMs) Ready() bool { return l.state.VMSnapshotReady() }

func (l liveVMs) VMManifests(namespaces []string) []model.File {
	objs := l.state.VMObjects(namespaces)
	out := make([]model.File, 0, len(objs))
	for i := range objs {
		content, err := cluster.ExportManifest(objs[i])
		if err != nil {
			log.Printf("live manifest %s/%s: %v", objs[i].Namespace, objs[i].Name, err)
			continue
		}
		out = append(out, model.File{Path: cluster.ExportPath(objs[i]), Content: content})
	}
	return out
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("dotvirt: %v", err)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}

	if cfg.InsecureTLS {
		git.AllowInsecureTLS() // dev: trust self-signed Forgejo Route cert
	} else if cfg.ForgeCA != "" {
		git.AllowCustomCA(cfg.ForgeCA) // verify the managed forge Route (ingress CA)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The one change bus: every source (k8s/argo reflectors, the git poll/webhook,
	// the proposals refresher) publishes a typed event here; every rebuild path
	// (the hub, the proposals refresher) subscribes to the kinds it needs. One
	// fan-out for all of them, no source-specific channels, no single-consumer
	// constraint.
	bus := eventbus.New()

	// Per-project git + the Forge API share ONE token source (resolved per call, so
	// an operator re-mint/rotation is picked up without restart).
	tokenSrc := cfg.ForgeTokenSource()
	repos := git.NewRepoSet(ctx, cfg.ForgeURL, cfg.GitUsername, tokenSrc, true, bus, cfg.GitPollInterval)

	draftStore, err := draft.Open(cfg.DraftDir)
	if err != nil {
		return err
	}
	forgeFactory := forge.NewFactory(cfg.ForgeURL, tokenSrc, cfg.InsecureTLS, cfg.ForgeCA).WithWebURL(cfg.ForgeWebURL)
	if forgeFactory == nil {
		log.Printf("forge not configured (DOTVIRT_FORGE_URL unset): propose will push-only, no PR will be created")
	}
	resolver := project.NewResolver(cfg.ProjectLabel, cfg.RepoAnnotation, cfg.ForgeURL)

	clusterFactory, err := cluster.NewFactory(cfg.Kubeconfig)
	if err != nil {
		return err
	}
	saCluster, err := clusterFactory.SA()
	if err != nil {
		return err
	}

	// One SA-maintained snapshot of live VM state + project topology, fed by
	// reflectors (not per-request fetches). Its reflectors publish LiveChanged (a
	// VM's live state moved) and NamespaceChanged to the bus, so the hub
	// re-broadcasts on any cluster change a frame shows; the read path filters this
	// shared snapshot per token instead of hitting the cluster.
	clusterSnapshot := clusterstate.New(saCluster, cfg.ProjectLabel, bus)
	clusterSnapshot.Run(ctx)

	// DRS status plane: the SA-watched KubeDescheduler snapshot behind GET
	// /api/drs. Discovery-gated - on a cluster without the descheduler operator
	// it stays a slow API probe, never an error loop.
	drsSnapshot := drs.NewSnapshot(saCluster)
	drsSnapshot.Run(ctx)

	// Networking read plane: the SA-watched network + fabric snapshot behind GET
	// /api/networks. Reflectors publish NetworkChanged so the catalog refreshes live;
	// discovery-gated like drs, so a cluster without OVN-K UDN or nmstate degrades
	// to empty rather than error-looping.
	netSnapshot := netstate.New(saCluster, bus)
	netSnapshot.Run(ctx)

	// Which parts of the console this cluster serves, from discovery.
	moduleSnapshot := modules.New(saCluster)
	moduleSnapshot.Run(ctx)

	// r-drbd's read plane: nothing runs until the module is on.
	replicationSnapshot := replication.New(saCluster, bus, func() bool {
		m, ok := moduleSnapshot.Get()
		return ok && m.ReplicatedStorage
	})
	replicationSnapshot.Run(ctx)

	// DeskuBe's read plane: nothing runs until the module is on.
	desktopsSnapshot := desktops.New(saCluster, bus, func() bool {
		m, ok := moduleSnapshot.Get()
		return ok && m.Desktops
	})
	desktopsSnapshot.Run(ctx)

	// Gateways' read plane: nothing runs until a GatewayClass exists.
	routesSnapshot := routes.New(saCluster, bus, func() bool {
		m, ok := moduleSnapshot.Get()
		return ok && m.Routes
	})
	routesSnapshot.Run(ctx)

	var argoSnapshot *argo.Snapshot
	var resyncer changeset.Resyncer
	if cfg.ArgoEnabled {
		saArgo, err := argo.NewSAClient(cfg.Kubeconfig)
		if err != nil {
			return err
		}
		// The Application snapshot is the drift plane: a reflector feeds an in-memory
		// store and publishes DriftChanged, so reads are lock-free and the hub
		// rebroadcasts when drift moves. It is also the resyncer - it owns the app
		// index the per-VM re-sync resolves.
		argoSnapshot = argo.NewSnapshot(saArgo, bus).WithWriteNamespace(cfg.ArgoNamespace)
		argoSnapshot.Run(ctx)
		resyncer = argoSnapshot
	}

	// Auth validates user tokens via TokenReview as dotvirt's SA.
	saKube, err := clusterFactory.SAKube()
	if err != nil {
		return err
	}
	authenticator := auth.New(saKube, []byte(cfg.SessionSecret))

	// OIDC sign-in or OpenShift SSO (optional, one of them): only token
	// acquisition changes - the token rides the same TokenReview + pass-through
	// path as a pasted one.
	var oidcFlow *auth.OIDC
	if cfg.OIDCIssuer != "" {
		switch {
		case cfg.PublicURL == "":
			log.Printf("oidc: -oidc-issuer set but -public-url empty; OIDC sign-in disabled")
		case cfg.OIDCClientID == "":
			log.Printf("oidc: -oidc-issuer set but -oidc-client-id empty; OIDC sign-in disabled")
		default:
			if cfg.OAuthClientID != "" {
				log.Printf("oidc: OIDC sign-in replaces OpenShift SSO; -oauth-client-id ignored")
			}
			oidcFlow = auth.NewOIDC(auth.OIDCConfig{
				Issuer:       cfg.OIDCIssuer,
				ClientID:     cfg.OIDCClientID,
				ClientSecret: cfg.OIDCClientSecret,
				RedirectURL:  strings.TrimRight(cfg.PublicURL, "/") + "/api/auth/callback",
				Scopes:       cfg.OIDCScopes,
				Name:         cfg.OIDCName,
				CAFile:       cfg.OIDCCA,
				InsecureTLS:  cfg.InsecureTLS,
			}, authenticator)
		}
	}
	var oauthFlow *auth.OAuth
	if cfg.OAuthClientID != "" && oidcFlow == nil {
		// The client and its redirect as the install declares them, the same
		// ones Finish SSO registers; the env the operator writes only when
		// they cannot be read (the console's role reads them).
		clientID, redirect, derr := api.SSOClientConfig(ctx, saCluster, ownNamespace(), cfg.OAuthClientID, cfg.PublicURL)
		if derr != nil {
			log.Printf("oauth: derive the SSO client from the install: %v; falling back to -oauth-client-id and -public-url", derr)
		}
		if redirect == "" {
			log.Printf("oauth: -oauth-client-id set but no redirect known (no install readable, -public-url empty); OpenShift SSO disabled")
		} else {
			oauthFlow, err = auth.NewOAuth(auth.OAuthConfig{
				ClientID:     clientID,
				ClientSecret: cfg.OAuthClientSecret,
				RedirectURL:  redirect,
				CAFile:       cfg.OAuthCA,
				InsecureTLS:  cfg.InsecureTLS,
			}, saKube, authenticator)
			if err != nil {
				return err
			}
		}
	}

	// Delegated pull requests: each user's own forge account, connected once
	// through the forge's OAuth2 provider, writes their changes. A setting
	// missing leaves no user a way to connect: their writes to project repos
	// are then refused, never made with dotvirt's account, which is said once
	// here and in every refusal.
	var delegated *auth.Forge
	var delegation changeset.Delegation
	if cfg.ForgeDelegated {
		switch {
		case cfg.PublicURL == "":
			delegation.Off = "the console has no public URL for the forge to send users back to: set spec.ingress.host, or wait for the router to assign the Route's host (-public-url)"
		case cfg.ForgeDelegatedURL == "":
			delegation.Off = "no forge URL to send users' tokens to: set spec.forge.url (-forge-delegated-url)"
		case cfg.ForgeClientID == "" || cfg.ForgeClientSecret == "" || cfg.ForgeAuthorizeURL == "":
			delegation.Off = "spec.forge.userAuth is incomplete (-forge-client-id, -forge-client-secret, -forge-authorize-url)"
		default:
			tokenURL := cfg.ForgeTokenURL
			if tokenURL == "" {
				tokenURL = strings.TrimRight(cfg.ForgeDelegatedURL, "/") + "/login/oauth/access_token"
			}
			delegated = auth.NewForge(auth.ForgeConfig{
				ClientID:     cfg.ForgeClientID,
				ClientSecret: cfg.ForgeClientSecret,
				AuthorizeURL: cfg.ForgeAuthorizeURL,
				TokenURL:     tokenURL,
				APIURL:       cfg.ForgeDelegatedURL,
				RedirectURL:  strings.TrimRight(cfg.PublicURL, "/") + "/api/forge/callback",
				CAFile:       cfg.ForgeCA,
				InsecureTLS:  cfg.InsecureTLS,
			}, authenticator)
			delegation.Accounts = delegated
			// Pages link where dotvirt's own links do.
			delegation.Forge = forge.NewFactory(cfg.ForgeDelegatedURL, forge.StaticToken(""), cfg.InsecureTLS, cfg.ForgeCA).
				WithWebURL(cmp.Or(cfg.ForgeWebURL, cfg.ForgeURL))
		}
		delegation.PlatformRepo = cfg.PlatformRepo
		if delegation.Off != "" {
			log.Printf("forge: -forge-delegated: %s; no user can connect a forge account, and their writes to project repos are refused", delegation.Off)
		}
	}

	// Typed-nil guard: a nil *argo.Snapshot in the interface would read as wired.
	var pruneSource changeset.PruneSource
	if argoSnapshot != nil {
		pruneSource = argoSnapshot
	}
	publishing := publish.Settings{Domain: cfg.PublishDomain, GatewayClass: cfg.PublishGatewayClass, Issuer: cfg.PublishIssuer, ProjectLabel: cfg.ProjectLabel, PortailNamespace: cfg.PublishPortailNamespace}
	coordinator := changeset.New(draftStore, repos, forgeFactory, resyncer,
		liveVMs{clusterSnapshot}, pruneSource, cfg.BaseBranch, cfg.ProposedBranch).WithSyncNamespace(cfg.ArgoNamespace).
		WithTenantKinds(cfg.TenantKinds).WithUsersOpeningPRs(cfg.UsersOpenPRs).WithPublishing(publishing).
		WithProductName(cfg.ProductName)
	if cfg.ForgeDelegated {
		coordinator.WithDelegation(delegation)
	}

	metricsClient, err := metrics.New(cfg.MetricsURL, cfg.MetricsTenancyURL, cfg.MetricsCA, cfg.InsecureTLS)
	if err != nil {
		return err
	}

	// Recent-tasks feed: handlers record imperative ops, the webhook + proposals
	// refresher record merged PRs; in-memory only (merges reseed from the forge).
	taskFeed := tasks.New(bus)

	server := api.NewServer(api.Deps{
		ClusterFactory: clusterFactory,
		State:          clusterSnapshot,
		Drift:          argoSnapshot,
		DRS:            drsSnapshot,
		Netstate:       netSnapshot,
		Modules:        moduleSnapshot,
		IndexBindings:  true,
		Replication:    replicationSnapshot,
		Gateways:       routesSnapshot,
		Desktops:       desktopsSnapshot,
		Bus:            bus,
		Resolver:       resolver,
		Repos:          repos,
		Metrics:        metricsClient,
		Tasks:          taskFeed,
		Forge:          forgeFactory,
		Reader:         coordinator,
		Draft:          coordinator,
		Auth:           authenticator,
		OAuth:          oauthFlow,
		OIDC:           oidcFlow,
		Delegated:      delegated,
		Config: api.Config{
			BaseBranch:        cfg.BaseBranch,
			ProposedBranch:    cfg.ProposedBranch,
			AllowOrigin:       cfg.UIOrigin,
			AppSetPluginToken: cfg.AppSetPluginToken,
			StaticDir:         cfg.StaticDir,
			WebhookSecret:     cfg.WebhookSecret,
			UploadProxyURL:    uploadProxyURL(ctx, cfg.UploadProxyURL, saCluster),
			ForgeAdmin:        cfg.ForgeAdmin,
			Install:           cfg.Install,
			OwnNamespace:      ownNamespace(),
			PlatformRepo:      cfg.PlatformRepo,
			ProjectLabel:      cfg.ProjectLabel,
			RepoAnnotation:    cfg.RepoAnnotation,
			TenantKinds:       cfg.TenantKinds,
			Publish:           publishing,
			Branding:          branding.Settings{ProductName: cfg.ProductName, Links: cfg.MastheadLinks, Dir: cfg.BrandingDir},
		},
	})

	coordinator.UseAuthorizer(server)
	coordinator.UseInstancetypes(server)
	coordinator.UseHolds(server)

	// Live inventory hub: each connection's frame is built under its identity (same
	// path as GET /api/inventory). It wakes on every kind that can alter a frame and
	// reconciles each identity to the version of those kinds - GitChanged only for
	// the repos its frame shows - so it coalesces by build duration (no debounce)
	// and never rebuilds a frame when nothing it depends on moved.
	inventoryKinds := []eventbus.Kind{
		eventbus.LiveChanged, eventbus.NamespaceChanged,
		eventbus.RBACChanged, eventbus.DriftChanged, eventbus.GitChanged, eventbus.ProposalsChanged,
		eventbus.NetworkChanged, eventbus.TaskChanged, eventbus.ReplicationChanged, eventbus.RoutesChanged, eventbus.DesktopsChanged,
		eventbus.TTLBackstop,
	}
	// SessionEnded only wakes the hub to close ended sessions' streams; no frame
	// depends on it, so it stays out of the levels.
	hubWake, _ := bus.Subscribe(append(inventoryKinds, eventbus.SessionEnded)...)
	hub := stream.NewHub(server.InventoryForIdentity, authenticator.SessionLive, hubWake, stream.RepoLevels(bus, eventbus.GitChanged, inventoryKinds...), cfg.UIOrigin)
	go hub.Run(ctx)
	server.UseStream(hub)

	// Open-PR lanes refresh in the background - on git head moves (GitChanged),
	// handler nudges, and a slow backstop - so the broadcast path never calls the
	// forge; a changed lane publishes ProposalsChanged.
	go server.RunProposalsRefresher(ctx, bus)

	// Frames also read TTL-cached authorization that no RBAC watch observes; while
	// a stream is open, the backstop rebuilds frames often enough that an expired
	// entry is re-evaluated within 7.5 minutes, or 45 seconds while an RBAC watch
	// is down (identical frames are not resent).
	go server.RunTTLBackstop(ctx)

	// VNC dials as the requesting user (KubeVirt RBAC gates the console); a
	// console closes when its session ends.
	vncWake, _ := bus.Subscribe(eventbus.SessionEnded)
	vnc := stream.NewVNCProxy(func(token string) (stream.VNCDialer, error) {
		return clusterFactory.For(token)
	}, authenticator.SessionLive, vncWake, cfg.UIOrigin)
	go vnc.Run(ctx)
	server.UseVNC(vnc)

	// Webhook auto-registration: one ORG-level hook so the forge delivers push/PR events
	// for every repo (the platform repo + all projects, present + future) to dotvirt, so
	// updates arrive in webhook latency rather than the next poll tick. It converges the
	// same org hook the operator manages. The forge usually runs in-cluster and can't reach
	// (or TLS-trust) the external Route, so delivery targets WebhookURL - the in-cluster
	// Service - when set, else PublicURL.
	webhookBase := cfg.WebhookURL
	if webhookBase == "" {
		webhookBase = cfg.PublicURL
	}
	switch {
	case webhookBase != "" && cfg.WebhookSecret != "" && forgeFactory != nil:
		target := strings.TrimRight(webhookBase, "/") + "/api/webhooks/forge"
		go ensureWebhooks(ctx, clusterSnapshot, server.AllProjects, forgeFactory, target, cfg.WebhookSecret, cfg.PlatformRepo)
	case forgeFactory != nil:
		// Forge is wired but there's no way for it to reach back: updates degrade silently
		// to the poll otherwise, so say so.
		log.Printf("webhook: no delivery base (set -public-url or -webhook-url) or secret; forge->dotvirt updates fall back to the %s git poll", cfg.GitPollInterval)
	}

	// Let the snapshot's initial LIST land before serving so the first inventory
	// isn't empty - but bound the wait: a degraded cluster must not block startup,
	// the snapshot fills in as reflectors sync and the hub pushes the update.
	syncCtx, cancelSync := context.WithTimeout(ctx, 10*time.Second)
	if err := clusterSnapshot.WaitForSync(syncCtx); err != nil && ctx.Err() == nil {
		log.Printf("cluster snapshot not synced yet (%v); serving and filling in as watches catch up", err)
	}
	// Let the drift snapshot's initial LIST land too, in the same bounded budget -
	// best-effort, so a slow/absent Argo never blocks startup (the hub re-pushes once
	// it syncs, and the inventory shows "sync temporarily unavailable" until then).
	if argoSnapshot != nil {
		if err := argoSnapshot.WaitForSync(syncCtx); err != nil && ctx.Err() == nil {
			log.Printf("argo drift snapshot not synced yet (%v); serving without drift until it catches up", err)
		}
	}
	cancelSync()

	srv := newHTTPServer(cfg.Addr, server.Handler())

	go func() {
		log.Printf("dotvirt listening on %s (project-label=%s)", cfg.Addr, cfg.ProjectLabel)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newHTTPServer bounds idle keep-alives only: WS and VNC connections are hijacked,
// so net/http's timeouts stop applying to them once upgraded. Read/WriteTimeout
// stay unset because they would cut long uploads and streamed responses.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// ensureWebhooks registers dotvirt's ORG-level forge webhook at startup and re-asserts it
// on a slow ticker (self-heal). One org hook covers the platform repo and every project,
// present and future, so no per-repo sweep is needed - and a from-scratch install with no
// project namespaces yet still gets its hook. It anchors the org on the platform repo, or on
// any resolved project when there is none. Failures are logged and retried next tick - a
// forge hiccup must not affect serving - except a refusal, which hookSweep holds.
func ensureWebhooks(ctx context.Context, state *clusterstate.State, projects func() []project.ProjectInfo, ff *forge.Factory, target, secret, platformRepo string) {
	// Anchoring on a project needs the namespace reflector's initial LIST; the platform
	// repo anchors the org hook without it, so only wait when there's no platform repo.
	if platformRepo == "" {
		syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = state.WaitForSync(syncCtx)
		cancel()
	}
	hooks := newHookSweep(ff, target, secret, log.Printf)
	sweep := func() {
		repos := []string{}
		if platformRepo != "" {
			repos = append(repos, platformRepo)
		}
		for _, p := range projects() {
			if p.Repo != "" {
				repos = append(repos, p.Repo)
			}
		}
		hooks.run(repos)
	}
	sweep()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// refusalRecheck is how long a refused hook registration is held: a fix made
// in the forge itself (the account made an owner, webhooks re-enabled) shows
// no change dotvirt can see, so the forge is asked again at this pace.
const refusalRecheck = time.Hour

// hookSweep is one process's registration of dotvirt's forge webhook. A forge
// that refuses dotvirt's account the owner's or a repo's webhooks (staff made
// the hooks, or the account owns no org) refuses again on every tick, so a
// refused scope is asked again only after refusalRecheck, or at once when the
// forge credential changes; a restart, which any change of the forge, the
// target or the secret is, starts over. An outage keeps the tick's retry.
type hookSweep struct {
	ff             *forge.Factory
	target, secret string
	// refused maps a scope ("org:<owner>" or "repo:<url>") to the refusal held.
	refused map[string]held
	// users remembers an owner confirmed as a user account as long as a
	// refusal, so a user-owned forge whose repo hooks are all held asks the
	// forge nothing. Never a missing owner: an org created later (a managed
	// forge's, after the first sweep) gets its hook on the next tick.
	users map[string]held
	now   func() time.Time
	logf  func(string, ...any)
}

// held is what an answer was met with: the credential and the time.
type held struct {
	cred string
	at   time.Time
}

func newHookSweep(ff *forge.Factory, target, secret string, logf func(string, ...any)) *hookSweep {
	return &hookSweep{ff: ff, target: target, secret: secret, refused: map[string]held{}, users: map[string]held{}, now: time.Now, logf: logf}
}

func (h *hookSweep) run(repos []string) {
	if len(repos) == 0 {
		return // nothing to anchor on yet; the next tick retries
	}
	// Any repo anchors the owner: they share it, so one org hook covers them all,
	// present and future.
	fc := h.ff.For(repos[0])
	if fc == nil {
		return
	}
	scope := "org:" + fc.Owner()
	if h.holds(h.refused, scope) {
		return
	}
	isOrg, err := h.ownerIsOrg(fc)
	if err != nil {
		h.failed(scope, "resolve owner kind (anchor "+repos[0]+")", err)
		return
	}
	if isOrg {
		if err := fc.EnsureOrgWebhook(h.target, h.secret); err != nil {
			h.failed(scope, "ensure org hook (anchor "+repos[0]+")", err)
			return
		}
		delete(h.refused, scope)
		return
	}
	// A user account has no hooks endpoint, so nothing can cover every repo at once;
	// register on each known repo instead. A repo added later is picked up by a
	// later tick rather than automatically, which is the cost of a user-owned forge.
	for _, repo := range repos {
		c := h.ff.For(repo)
		scope := "repo:" + repo
		if c == nil || h.holds(h.refused, scope) {
			continue
		}
		if err := c.EnsureWebhook(h.target, h.secret); err != nil {
			h.failed(scope, "ensure repo hook ("+repo+")", err)
			continue
		}
		delete(h.refused, scope)
	}
}

// ownerIsOrg is fc's owner kind. A confirmed user account is remembered for
// refusalRecheck and the credential; an org, or no owner yet, is asked again.
func (h *hookSweep) ownerIsOrg(fc *forge.Client) (bool, error) {
	if h.holds(h.users, fc.Owner()) {
		return false, nil
	}
	isOrg, err := fc.OwnerIsOrg()
	if err != nil || isOrg {
		return isOrg, err
	}
	if isUser, uerr := fc.OwnerIsUser(); uerr == nil && isUser {
		h.users[fc.Owner()] = held{cred: h.ff.CredentialFingerprint(), at: h.now()}
	}
	return false, nil
}

// holds reports whether m holds key under the current credential and within
// refusalRecheck.
func (h *hookSweep) holds(m map[string]held, key string) bool {
	e, ok := m[key]
	return ok && e.cred == h.ff.CredentialFingerprint() && h.now().Sub(e.at) < refusalRecheck
}

// failed logs err; a refusal is also held, and logged once with the forge's
// own reason and what to do.
func (h *hookSweep) failed(scope, what string, err error) {
	status := forge.RefusedStatus(err)
	if status == 0 {
		h.logf("webhook: %s: %v", what, err)
		return
	}
	h.refused[scope] = held{cred: h.ff.CredentialFingerprint(), at: h.now()}
	reason := forge.RefusalText(err)
	if reason == "" {
		reason = http.StatusText(status)
	}
	if status == http.StatusUnauthorized {
		h.logf("webhook: %s: the forge rejected dotvirt's forge token (HTTP 401: %s): replace the forge token; "+
			"rechecked hourly, and at once when the token changes", what, reason)
		return
	}
	grant := "an owner of the org"
	if strings.HasPrefix(scope, "repo:") {
		grant = "an admin of the repo"
	}
	h.logf("webhook: %s: the forge refused (HTTP %d: %s); rechecked hourly, and at once when the forge token changes. "+
		"Make dotvirt's forge account %s, or create the hook by hand as a Forgejo (Gitea) webhook: "+
		"an org hook, a system hook, or a hook on each project repo (repos created later need their own): URL %s, "+
		"content type application/json, events push and pull_request, secret the operator's dotvirt-webhook Secret, key secret "+
		"(oc extract secret/dotvirt-webhook -n <dotvirt namespace> --keys=secret --to=-)",
		what, status, reason, grant, h.target)
}

// uploadProxyURL is the flag's value, else the proxy CDI publishes. A cluster
// without CDI, or a read that fails, leaves image upload off.
func uploadProxyURL(ctx context.Context, flag string, sa *cluster.Client) string {
	if flag != "" {
		return flag
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url, err := sa.UploadProxyURL(ctx)
	if err != nil {
		log.Printf("upload: CDI's upload proxy is unknown, image upload stays off: %v", err)
	}
	return url
}

// ownNamespace is the namespace this pod runs in, "" out of cluster. It is
// read from the ServiceAccount mount, which the operator guard keeps from the
// operator: the env the operator writes cannot steer it.
func ownNamespace() string {
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
