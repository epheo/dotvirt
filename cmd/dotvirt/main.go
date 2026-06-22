// Command dotvirt serves a vCenter-like WebUI that edits per-project git repos of
// KubeVirt manifests and reads live state from a cluster and ArgoCD. It is a thin
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
	"github.com/epheo/dotvirt/internal/draft"
	"github.com/epheo/dotvirt/internal/eventbus"
	"github.com/epheo/dotvirt/internal/export"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/stream"
	"github.com/epheo/dotvirt/pkg/forge"
)

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
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The one change bus: every source (k8s/argo reflectors, the git poll/webhook,
	// the proposals refresher) publishes a typed event here; every rebuild path (the
	// hub, the exporter, the proposals refresher, the visibility-cache invalidator)
	// subscribes to the kinds it needs. One fan-out for all of them — no source-
	// specific channels, no single-consumer constraint.
	bus := eventbus.New()

	// Per-project git + the Forge API share ONE token source (resolved per call, so
	// an operator re-mint/rotation is picked up without restart).
	tokenSrc := cfg.ForgeTokenSource()
	repos := git.NewRepoSet(ctx, cfg.GitUsername, tokenSrc, cfg.Push, bus, cfg.GitPollInterval)

	draftStore, err := draft.Open(cfg.DraftDir)
	if err != nil {
		return err
	}
	forgeFactory := forge.NewFactoryFn(cfg.ForgeURL, tokenSrc, cfg.InsecureTLS)
	if forgeFactory == nil {
		log.Printf("forge not configured (DOTVIRT_FORGE_URL unset): propose will push-only, no PR will be created")
	}
	resolver := project.NewResolver(cfg.ProjectLabel, cfg.RepoAnnotation)

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

	var argoSnapshot *argo.Snapshot
	var resyncer changeset.Resyncer
	if cfg.ArgoEnabled {
		argoFactory, err := argo.NewFactory(cfg.Kubeconfig)
		if err != nil {
			return err
		}
		saArgo, err := argoFactory.SA()
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

	coordinator := changeset.New(draftStore, repos, forgeFactory, resyncer,
		cfg.BaseBranch, cfg.ProposedBranch, cfg.RunningBranch)

	metricsClient, err := metrics.New(cfg.MetricsURL, cfg.MetricsCA, cfg.InsecureTLS)
	if err != nil {
		return err
	}

	server := api.NewServer(api.Deps{
		ClusterFactory: clusterFactory,
		State:          clusterSnapshot,
		Drift:          argoSnapshot,
		Bus:            bus,
		Resolver:       resolver,
		Repos:          repos,
		Metrics:        metricsClient,
		Draft:          coordinator,
		Auth:           authenticator,
		Config: api.Config{
			BaseBranch:        cfg.BaseBranch,
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

	// Per-project running-branch export, on the SA identity. Topology AND the VM
	// objects come from the snapshot — an export tick touches the cluster zero times.
	exporter := export.New(clusterSnapshot, resolver, repos, cfg.RunningBranch)
	go exporter.Run(ctx, cfg.ExportInterval, bus)

	// Webhook auto-registration: ensure every project repo delivers push/PR events
	// to dotvirt, so updates arrive in webhook latency rather than the next poll tick.
	// The forge usually runs in-cluster and can't reach (or TLS-trust) the external
	// Route, so delivery targets WebhookURL — the in-cluster Service — when set, else
	// PublicURL. Idempotent per sweep; new projects are picked up by the re-sweep.
	webhookBase := cfg.WebhookURL
	if webhookBase == "" {
		webhookBase = cfg.PublicURL
	}
	if webhookBase != "" && cfg.WebhookSecret != "" && forgeFactory != nil {
		target := strings.TrimRight(webhookBase, "/") + "/api/webhooks/forge"
		go ensureWebhooks(ctx, clusterSnapshot, resolver, forgeFactory, target, cfg.WebhookSecret)
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

// ensureWebhooks sweeps the resolved projects and registers dotvirt's webhook
// on each repo, at startup and on a slow ticker (new projects join the next
// sweep). Failures are logged and retried next sweep — a forge hiccup must not
// affect serving.
func ensureWebhooks(ctx context.Context, state *clusterstate.State, resolver *project.Resolver, ff *forge.Factory, target, secret string) {
	// The first sweep is only useful once the namespace reflector has its
	// initial LIST — before that the project set reads empty and every hook
	// would wait for the next ticker.
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_ = state.WaitForSync(syncCtx)
	cancel()
	sweep := func() {
		for _, p := range resolver.Resolve(state.Namespaces(), nil) {
			if p.Repo == "" {
				continue
			}
			fc := ff.For(p.Repo)
			if fc == nil {
				continue
			}
			if err := fc.EnsureWebhook(target, secret); err != nil {
				log.Printf("webhook: ensure on %s: %v", p.Repo, err)
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
