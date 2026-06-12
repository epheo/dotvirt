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

	// Per-project git: one read mirror + writable view per repo URL, all on the
	// single Forgejo credential. changed will drive the live hub in a later step;
	// the per-repo poll signals it non-blockingly, so it's safe to leave unread.
	changed := make(chan struct{}, 1)
	repos := git.NewRepoSet(ctx, cfg.GitUsername, cfg.GitToken, cfg.Push, changed, cfg.GitPollInterval)

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

	server := api.NewServer(api.Deps{
		ClusterFactory: clusterFactory,
		State:          clusterSnapshot,
		Drift:          driftCache,
		Resolver:       resolver,
		Repos:          repos,
		Metrics:        metrics.New(cfg.MetricsURL, cfg.InsecureTLS),
		Draft:          coordinator,
		Auth:           authenticator,
		Config: api.Config{
			BaseBranch:        cfg.BaseBranch,
			AllowOrigin:       cfg.UIOrigin,
			AppSetPluginToken: cfg.AppSetPluginToken,
		},
	})

	// WebSocket origin policy: same-origin + the configured UI origin (CORS doesn't
	// cover WS handshakes, so this is the only origin gate for the stream/VNC sockets).
	stream.SetAllowedOrigin(cfg.UIOrigin)

	// Live inventory hub: each subscriber's frame is built under their identity
	// (same path as GET /api/inventory). Fed by the SA watches + the RepoSet's
	// per-repo git poll via the shared `changed` channel.
	hub := stream.NewHub(server.InventoryForIdentity)
	go hub.Run(ctx)
	go forward(ctx, changed, hub.Changed())
	server.UseStream(hub)

	// Flush the open-PR cache on any git head move, so the lane is fresh on a real
	// propose/merge while idle heartbeats don't re-poll the forge. Set before serving
	// (and thus before any request starts a poll goroutine).
	repos.SetOnChange(server.InvalidateProposals)

	// VNC dials as the requesting user (KubeVirt RBAC gates the console).
	server.UseVNC(stream.NewVNCProxy(func(token string) (stream.VNCDialer, error) {
		return clusterFactory.For(token)
	}))

	// Per-project running-branch export, on the SA identity. Topology comes from the
	// snapshot; the authoritative VM objects are listed with the SA client.
	exporter := export.New(saCluster, clusterSnapshot, resolver, repos, cfg.RunningBranch)
	go exporter.Run(ctx, cfg.ExportInterval)

	if saArgo != nil {
		saArgo.Watch(ctx, hub.Changed()) // push on Application drift changes
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

// forward relays change signals from src (the RepoSet's git-poll channel) to dst
// (the hub's). Both are coalescing 1-buffered channels; a dropped duplicate is
// fine since the hub recomputes the full state on any signal.
func forward(ctx context.Context, src <-chan struct{}, dst chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-src:
			select {
			case dst <- struct{}{}:
			default:
			}
		}
	}
}
