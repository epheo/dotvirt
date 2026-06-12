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
	"github.com/epheo/dotvirt/internal/export"
	"github.com/epheo/dotvirt/internal/forge"
	"github.com/epheo/dotvirt/internal/git"
	"github.com/epheo/dotvirt/internal/metrics"
	"github.com/epheo/dotvirt/internal/project"
	"github.com/epheo/dotvirt/internal/stream"
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

	// The one change bus: every source (git polls, k8s reflectors, the argo watch,
	// the proposals refresher) signals this 1-buffered channel non-blockingly, and
	// the hub is its only consumer — it coalesces bursts and rebroadcasts inventory.
	// gitChanged is the git-only side of the same poll, consumed by the proposals
	// refresher (which must not re-query the forge on every cluster event).
	changed := make(chan struct{}, 1)
	gitChanged := make(chan struct{}, 1)

	// Per-project git: one read mirror + writable view per repo URL, all on the
	// single Forgejo credential.
	repos := git.NewRepoSet(ctx, cfg.GitUsername, cfg.GitToken, cfg.Push, changed, gitChanged, cfg.GitPollInterval)

	draftStore, err := draft.Open(cfg.DraftDir)
	if err != nil {
		return err
	}
	forgeFactory := forge.NewFactory(cfg.ForgeURL, cfg.ForgeToken, cfg.InsecureTLS)
	if forgeFactory == nil {
		log.Printf("forge not configured (DOTVIRT_FORGE_URL/DOTVIRT_FORGE_TOKEN unset): propose will push-only, no PR will be created")
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
	// reflectors (not per-request fetches). It signals the same `changed` channel
	// the git poll uses, so the hub re-broadcasts on any cluster change; the read
	// path filters this shared snapshot per token instead of hitting the cluster.
	clusterSnapshot := clusterstate.New(saCluster, cfg.ProjectLabel, changed)
	clusterSnapshot.Run(ctx)

	var saArgo *argo.Client
	var driftCache *argo.DriftCache
	var resyncer changeset.Resyncer
	if cfg.ArgoEnabled {
		argoFactory, err := argo.NewFactory(cfg.Kubeconfig)
		if err != nil {
			return err
		}
		if saArgo, err = argoFactory.SA(); err != nil {
			return err
		}
		resyncer = saArgo
		// Drift is read once per short window and shared across all subscribers
		// (the inventory is rebuilt per subscriber on every change).
		driftCache = argo.NewDriftCache(saArgo, 5*time.Second)
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
		Drift:          driftCache,
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
		},
	})

	// WebSocket origin policy: same-origin + the configured UI origin (CORS doesn't
	// cover WS handshakes, so this is the only origin gate for the stream/VNC sockets).
	stream.SetAllowedOrigin(cfg.UIOrigin)

	// Live inventory hub: each subscriber's frame is built under their identity
	// (same path as GET /api/inventory). It consumes the shared change bus directly.
	hub := stream.NewHub(server.InventoryForIdentity, changed)
	go hub.Run(ctx)
	server.UseStream(hub)

	// Open-PR lanes refresh in the background — on git head moves, handler nudges,
	// and a slow backstop — so the broadcast path never calls the forge.
	go server.RunProposalsRefresher(ctx, gitChanged, changed)

	// VNC dials as the requesting user (KubeVirt RBAC gates the console).
	server.UseVNC(stream.NewVNCProxy(func(token string) (stream.VNCDialer, error) {
		return clusterFactory.For(token)
	}))

	// Per-project running-branch export, on the SA identity. Topology AND the VM
	// objects come from the snapshot — an export tick touches the cluster zero times.
	exporter := export.New(clusterSnapshot, resolver, repos, cfg.RunningBranch)
	go exporter.Run(ctx, cfg.ExportInterval)

	if saArgo != nil {
		saArgo.Watch(ctx, changed) // push on Application drift changes
	}

	// Webhook auto-registration: ensure every project repo delivers push/PR
	// events to dotvirt's public URL, so updates arrive in webhook latency
	// rather than the next poll tick. Idempotent per sweep; new projects are
	// picked up by the periodic re-sweep.
	if cfg.PublicURL != "" && cfg.WebhookSecret != "" && forgeFactory != nil {
		target := strings.TrimRight(cfg.PublicURL, "/") + "/api/webhooks/forge"
		go ensureWebhooks(ctx, clusterSnapshot, resolver, forgeFactory, target, cfg.WebhookSecret)
	}

	// Let the snapshot's initial LIST land before serving so the first inventory
	// isn't empty — but bound the wait: a degraded cluster must not block startup,
	// the snapshot fills in as reflectors sync and the hub pushes the update.
	syncCtx, cancelSync := context.WithTimeout(ctx, 10*time.Second)
	if err := clusterSnapshot.WaitForSync(syncCtx); err != nil && ctx.Err() == nil {
		log.Printf("cluster snapshot not synced yet (%v); serving and filling in as watches catch up", err)
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
