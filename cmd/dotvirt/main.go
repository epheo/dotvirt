// Command dotvirt serves a web console, familiar to vSphere admins, that edits
// per-project git repos of KubeVirt manifests and reads live state from a cluster
// and ArgoCD. It is a thin
// multi-tenant lens: every request runs under the caller's own k8s token, and a
// project is a set of namespaces (a cluster fact) backed by its own git repo.
package main

import (
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
	"github.com/epheo/dotvirt/internal/changeset"
	"github.com/epheo/dotvirt/internal/cluster"
	"github.com/epheo/dotvirt/internal/clusterstate"
	"github.com/epheo/dotvirt/internal/config"
	"github.com/epheo/dotvirt/internal/desched"
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/netstate"
	"github.com/epheo/dotvirt/internal/project"
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

func (l liveVMs) VMManifests(namespaces []string) []changeset.LiveManifest {
	objs := l.state.VMObjects(namespaces)
	out := make([]changeset.LiveManifest, 0, len(objs))
	for i := range objs {
		content, err := cluster.ExportManifest(objs[i])
		if err != nil {
			log.Printf("live manifest %s/%s: %v", objs[i].Namespace, objs[i].Name, err)
			continue
		}
		out = append(out, changeset.LiveManifest{Path: cluster.ExportPath(objs[i]), Content: content})
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
	// the proposals refresher) publishes a typed event here; every rebuild path (the
	// hub, the proposals refresher, the visibility-cache invalidator)
	// subscribes to the kinds it needs. One fan-out for all of them — no source-
	// specific channels, no single-consumer constraint.
	bus := eventbus.New()

	// Per-project git + the Forge API share ONE token source (resolved per call, so
	// an operator re-mint/rotation is picked up without restart).
	tokenSrc := cfg.ForgeTokenSource()
	repos := git.NewRepoSet(ctx, cfg.GitUsername, tokenSrc, true, bus, cfg.GitPollInterval)

	draftStore, err := draft.Open(cfg.DraftDir)
	if err != nil {
		return err
	}
	forgeFactory := forge.NewFactoryFnCA(cfg.ForgeURL, tokenSrc, cfg.InsecureTLS, cfg.ForgeCA)
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
	// reflectors (not per-request fetches). Its reflectors publish VMSpecChanged /
	// LiveChanged (VMs/VMIs) and NamespaceChanged to the bus, so the hub re-broadcasts
	// on any cluster change; the read path filters this shared snapshot per token
	// instead of hitting the cluster.
	clusterSnapshot := clusterstate.New(saCluster, cfg.ProjectLabel, bus)
	clusterSnapshot.Run(ctx)

	// DRS status plane: the SA-watched KubeDescheduler snapshot behind GET
	// /api/drs. Discovery-gated — on a cluster without the descheduler operator
	// it stays a slow API probe, never an error loop.
	deschedSnapshot := desched.New(saCluster)
	deschedSnapshot.Run(ctx)

	// Networking read plane: the SA-watched port-group + fabric snapshot behind GET
	// /api/networks. Reflectors publish NetworkChanged so the catalog refreshes live;
	// discovery-gated like desched, so a cluster without OVN-K UDN or nmstate degrades
	// to empty rather than error-looping.
	netSnapshot := netstate.New(saCluster, bus)
	netSnapshot.Run(ctx)

	var argoSnapshot *argo.Snapshot
	var resyncer changeset.Resyncer
	if cfg.ArgoEnabled {
		saArgo, err := argo.NewSAClient(cfg.Kubeconfig)
		if err != nil {
			return err
		}
		// The Application snapshot is the drift plane: a reflector feeds an in-memory
		// store and publishes DriftChanged, so reads are lock-free and the hub
		// rebroadcasts on every Application move. It is also the resyncer — it owns the
		// app index the per-VM re-sync resolves.
		argoSnapshot = argo.NewSnapshot(saArgo, bus)
		argoSnapshot.Run(ctx)
		resyncer = argoSnapshot
	}

	// Auth validates user tokens via TokenReview as dotvirt's SA.
	saKube, err := clusterFactory.SAKube()
	if err != nil {
		return err
	}
	authenticator := auth.New(saKube, []byte(cfg.SessionSecret))

	// OpenShift SSO (optional): only token acquisition changes — the access token
	// rides the same TokenReview + cookie + pass-through path as a pasted one.
	var oauthFlow *auth.OAuth
	if cfg.OAuthClientID != "" {
		if cfg.PublicURL == "" {
			log.Printf("oauth: -oauth-client-id set but -public-url empty; OpenShift SSO disabled")
		} else {
			oauthFlow, err = auth.NewOAuth(auth.OAuthConfig{
				ClientID:     cfg.OAuthClientID,
				ClientSecret: cfg.OAuthClientSecret,
				RedirectURL:  strings.TrimRight(cfg.PublicURL, "/") + "/api/auth/callback",
				CAFile:       cfg.OAuthCA,
				InsecureTLS:  cfg.InsecureTLS,
			}, saKube, authenticator)
			if err != nil {
				return err
			}
		}
	}

	// Typed-nil guard: a nil *argo.Snapshot in the interface would read as wired.
	var pruneSource changeset.PruneSource
	if argoSnapshot != nil {
		pruneSource = argoSnapshot
	}
	coordinator := changeset.New(draftStore, repos, forgeFactory, resyncer,
		liveVMs{clusterSnapshot}, pruneSource, cfg.BaseBranch, cfg.ProposedBranch)

	metricsClient, err := metrics.New(cfg.MetricsURL, cfg.MetricsCA, cfg.InsecureTLS)
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
		Desched:        deschedSnapshot,
		Netstate:       netSnapshot,
		Bus:            bus,
		Resolver:       resolver,
		Repos:          repos,
		Metrics:        metricsClient,
		Tasks:          taskFeed,
		Draft:          coordinator,
		Auth:           authenticator,
		OAuth:          oauthFlow,
		Config: api.Config{
			BaseBranch:        cfg.BaseBranch,
			ProposedBranch:    cfg.ProposedBranch,
			AllowOrigin:       cfg.UIOrigin,
			AppSetPluginToken: cfg.AppSetPluginToken,
			StaticDir:         cfg.StaticDir,
			WebhookSecret:     cfg.WebhookSecret,
			UploadProxyURL:    cfg.UploadProxyURL,
			PlatformRepo:      cfg.PlatformRepo,
		},
	})

	// WebSocket origin policy: same-origin + the configured UI origin (CORS doesn't
	// cover WS handshakes, so this is the only origin gate for the stream/VNC sockets).
	stream.SetAllowedOrigin(cfg.UIOrigin)

	// Live inventory hub: each connection's frame is built under its identity (same
	// path as GET /api/inventory). It wakes on every kind that can alter a frame and
	// reconciles to the summed version of those kinds — so it coalesces by build
	// duration (no debounce) and never recomputes when nothing it depends on moved.
	inventoryKinds := []eventbus.Kind{
		eventbus.VMSpecChanged, eventbus.LiveChanged, eventbus.NamespaceChanged,
		eventbus.RBACChanged, eventbus.DriftChanged, eventbus.GitChanged, eventbus.ProposalsChanged,
		eventbus.NetworkChanged, eventbus.TaskChanged,
	}
	hubWake, _ := bus.Subscribe(inventoryKinds...)
	hub := stream.NewHub(server.InventoryForIdentity, hubWake, func() uint64 { return bus.Version(inventoryKinds...) })
	go hub.Run(ctx)
	server.UseStream(hub)

	// Open-PR lanes refresh in the background — on git head moves (GitChanged),
	// handler nudges, and a slow backstop — so the broadcast path never calls the
	// forge; a changed lane publishes ProposalsChanged.
	go server.RunProposalsRefresher(ctx, bus)

	// VNC dials as the requesting user (KubeVirt RBAC gates the console).
	server.UseVNC(stream.NewVNCProxy(func(token string) (stream.VNCDialer, error) {
		return clusterFactory.For(token)
	}))

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
		go ensureWebhooks(ctx, clusterSnapshot, resolver, forgeFactory, target, cfg.WebhookSecret, cfg.PlatformRepo)
	case forgeFactory != nil:
		// Forge is wired but there's no way for it to reach back: updates degrade silently
		// to the poll otherwise, so say so.
		log.Printf("webhook: no delivery base (set -public-url or -webhook-url) or secret; forge->dotvirt updates fall back to the %s git poll", cfg.GitPollInterval)
	}

	// Let the snapshot's initial LIST land before serving so the first inventory
	// isn't empty — but bound the wait: a degraded cluster must not block startup,
	// the snapshot fills in as reflectors sync and the hub pushes the update.
	syncCtx, cancelSync := context.WithTimeout(ctx, 10*time.Second)
	if err := clusterSnapshot.WaitForSync(syncCtx); err != nil && ctx.Err() == nil {
		log.Printf("cluster snapshot not synced yet (%v); serving and filling in as watches catch up", err)
	}
	// Let the drift snapshot's initial LIST land too, in the same bounded budget —
	// best-effort, so a slow/absent Argo never blocks startup (the hub re-pushes once
	// it syncs, and the inventory shows "sync temporarily unavailable" until then).
	if argoSnapshot != nil {
		if err := argoSnapshot.WaitForSync(syncCtx); err != nil && ctx.Err() == nil {
			log.Printf("argo drift snapshot not synced yet (%v); serving without drift until it catches up", err)
		}
	}
	cancelSync()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

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

// ensureWebhooks registers dotvirt's ORG-level forge webhook at startup and re-asserts it
// on a slow ticker (self-heal). One org hook covers the platform repo and every project,
// present and future, so no per-repo sweep is needed - and a from-scratch install with no
// project namespaces yet still gets its hook. It anchors the org on the platform repo, or on
// any resolved project when there is none. Failures are logged and retried next tick - a
// forge hiccup must not affect serving.
func ensureWebhooks(ctx context.Context, state *clusterstate.State, resolver *project.Resolver, ff *forge.Factory, target, secret, platformRepo string) {
	// Anchoring on a project needs the namespace reflector's initial LIST; the platform
	// repo anchors the org hook without it, so only wait when there's no platform repo.
	if platformRepo == "" {
		syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = state.WaitForSync(syncCtx)
		cancel()
	}
	sweep := func() {
		repos := []string{}
		if platformRepo != "" {
			repos = append(repos, platformRepo)
		}
		for _, p := range resolver.Resolve(state.Namespaces(), nil) {
			if p.Repo != "" {
				repos = append(repos, p.Repo)
			}
		}
		if len(repos) == 0 {
			return // nothing to anchor on yet; the next tick retries
		}
		// Any repo anchors the owner: they share it, so one org hook covers them all,
		// present and future.
		fc := ff.For(repos[0])
		if fc == nil {
			return
		}
		isOrg, err := fc.OwnerIsOrg()
		if err != nil {
			log.Printf("webhook: resolve owner kind (anchor %s): %v", repos[0], err)
			return
		}
		if isOrg {
			if err := fc.EnsureOrgWebhook(target, secret); err != nil {
				log.Printf("webhook: ensure org hook (anchor %s): %v", repos[0], err)
			}
			return
		}
		// A user account has no hooks endpoint, so nothing can cover every repo at once;
		// register on each known repo instead. A repo added later is picked up by a
		// later tick rather than automatically, which is the cost of a user-owned forge.
		for _, repo := range repos {
			c := ff.For(repo)
			if c == nil {
				continue
			}
			if err := c.EnsureWebhook(target, secret); err != nil {
				log.Printf("webhook: ensure repo hook (%s): %v", repo, err)
			}
		}
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
