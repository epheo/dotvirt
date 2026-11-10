import {
	api,
	Unauthorized,
	type Alert,
	withRetry,
	type Inventory,
	type NetworkInventory,
	type Options,
	type PolicyInventory,
	type StreamStatus,
	type TaskEntry,
	type VM,
} from '#lib/api.js';
import type { Authoring } from '#lib/authoring.js';
import { friendlyError } from '#lib/format.js';
import { projectNS, repoGate, repoReadGate, type Gate } from '#lib/gates.js';
import { deriveIssues, deriveSuggestions } from '#lib/issues.js';
import { membership, networkProblems } from '#lib/netmap.js';
import { prKey } from '#lib/review.js';
import { share } from '#lib/share.js';
import { modules } from './modules.svelte';

// Only the server's yes authors: an absent verdict (the review failed this
// frame) fails closed, like the stage routes behind it.
const authorable = (n: { canAuthor?: boolean }) => n.canAuthor === true;
const vmAuthorable = (n: { canAuthorVMs?: boolean }) => n.canAuthorVMs === true;

// The live cluster read layer: the WS inventory snapshot plus the once-per-
// session networking inventory (GET /api/networks). Everything else the UI
// shows about cluster state derives from these two.
class InventoryStore {
	// Raw, not a deep proxy: apply() replaces the whole frame and nothing
	// mutates it in place, so per-field signals would only cost a re-proxy of
	// every VM on every frame.
	inventory = $state.raw<Inventory | null>(null);
	error = $state('');
	// The live stream's health; the snapshot above is stale unless 'live'.
	stream = $state<StreamStatus>('connecting');
	readonly projects = $derived(this.inventory?.projects ?? []);
	// The networking read layer: the network catalog (so raw OVN-K refs render
	// as catalog networks) plus the physical fabric (uplinks + node NICs).
	// Changes rarely; backend caches 60s.
	netInv = $state<NetworkInventory | null>(null);
	// The policy plane behind the Security view; fetched beside netInv (both ride
	// the networksVersion refetch).
	polInv = $state<PolicyInventory | null>(null);
	// The last networks/policies pull's terminal failure; '' once one succeeds.
	netError = $state('');
	// A networks/policies pull is in flight: the banner's Retry waits on it.
	netBusy = $state(false);
	#netPull: AbortController | null = null;

	readonly networks = $derived(this.netInv?.networks ?? []);
	readonly policies = $derived(this.polInv?.policies ?? []);
	readonly uplinks = $derived(this.netInv?.uplinks ?? []);
	readonly physicalAdapters = $derived(this.netInv?.physicalAdapters ?? []);
	readonly nmstatePresent = $derived(this.netInv?.nmstatePresent ?? false);

	// All VMs across the 3-level tree (project -> namespace -> vm).
	readonly allVMs = $derived(this.projects.flatMap((p) => p.namespaces.flatMap((n) => n.vms)));
	readonly vmCount = $derived(this.allVMs.length);
	// Firing cluster alerts, polled out of band; null = the alerts feed is
	// unavailable (metrics off or unreachable), [] = none firing.
	alerts = $state.raw<Alert[] | null>([]);
	// Standing problems and firing alerts (the issues plane), derived once per
	// frame for the header count, the tree badges and the Summary card.
	// Which VMs sit on which network, and the networking problems the catalog
	// shows: uplinks failing on hosts, subnets that overlap where they can meet.
	readonly netMembers = $derived(membership(this.allVMs, this.networks));
	readonly netProblems = $derived(networkProblems(this.networks, this.uplinks, this.netMembers));
	readonly issues = $derived(
		deriveIssues(this.inventory, {
			alerts: this.alerts ?? [],
			canRepair: (p) => repoGate(p).ok,
			network: this.netProblems,
			anyTenantKinds: modules.value?.anyTenantKinds === true,
		}),
	);
	// What is worth doing but not wrong: adoptable namespaces and VMs.
	readonly suggestions = $derived(deriveSuggestions(this.inventory, (ns) => this.canAuthorVMs(ns)));
	// Open PRs across the user's projects ride the live inventory stream, so a PR
	// merged anywhere repaints the dock + Changes pane with no manual refresh.
	readonly proposals = $derived(this.inventory?.proposals ?? []);
	// Existing tenants proposed for adoption (namespaces with VMs, no project
	// label). The backend only fills this for callers with namespace-create
	// authority, so non-empty implies the adopt action is allowed.
	readonly adoptable = $derived(this.inventory?.adoptable ?? []);
	readonly projectNames = $derived(this.projects.map((p) => p.name));
	// A stable primitive key for the SET of project names: $derived arrays are a
	// new reference every inventory frame, which would re-fire effects on every VM
	// state change. Keying on this string fires them only when the set changes.
	readonly projectKey = $derived([...this.projectNames].sort().join('\0'));
	// The same trick for the SET of open PR lanes: a propose moves staged items
	// into a PR and a merge/close clears the lane - possibly from another tab.
	readonly proposalsKey = $derived(
		this.proposals
			.map((p) => `${prKey(p.project, p.prNumber)}#${p.head ?? ''}`)
			.sort()
			.join('\0'),
	);
	// Watermark for the out-of-band network catalog, off the live frame: bumps when
	// GitOps state or a repo head moves. A stable primitive (not the per-frame inventory
	// object), so an effect keyed on it re-pulls /api/networks only when networks may
	// have changed - not on every VM-state frame.
	readonly networksVersion = $derived(this.inventory?.networksVersion ?? 0);
	// The cluster options catalog (instance types, storage classes, images):
	// read views resolve instancetype sizing and the default StorageClass from
	// it without a per-view fetch. loadOptions is its one writer.
	options = $state<Options | null>(null);
	optionsError = $state(''); // the last pull's terminal failure; '' once one succeeds
	#optionsPull: Promise<void> | null = null;
	// Bumped by reset(), so a pull started before a sign-out drops its late
	// response instead of repopulating the reset store.
	#generation = 0;
	// The cluster's default StorageClass name, so the storage lens groups a
	// classless disk under the real class instead of a "(cluster default)"
	// placeholder; '' until the catalog loads.
	readonly defaultStorageClass = $derived(
		this.options?.storageClasses?.find((s) => s.default)?.name ?? '',
	);
	// The recent-tasks feed (GET /api/tasks), re-pulled when tasksVersion bumps -
	// every browser sees every admin's ops and merges, with real attribution.
	taskFeed = $state<TaskEntry[]>([]);
	tasksError = $state(''); // the last feed pull's failure; '' once one succeeds
	readonly tasksVersion = $derived(this.inventory?.tasksVersion ?? 0);
	// The replication pulls' watermark; absent while no r-drbd is served.
	readonly replicationVersion = $derived(this.inventory?.replicationVersion ?? 0);
	// The Gateway pulls' watermark; absent while no GatewayClass exists.
	readonly routesVersion = $derived(this.inventory?.routesVersion ?? 0);
	// Namespaces in projects that have a repo, whether or not the caller may author there.
	readonly repoNamespaces = $derived(
		this.projects.filter((p) => p.repo).flatMap((p) => p.namespaces.map((n) => n.namespace)),
	);
	// Namespaces the caller may stage into: repo-backed and canAuthor, the gate
	// every stage route enforces. Every author-gated action reads this set,
	// through canAuthor or here.
	readonly namespaces = $derived(
		this.projects
			.filter((p) => p.repo)
			.flatMap((p) => p.namespaces.filter(authorable).map((n) => n.namespace)),
	);
	readonly #authorSet = $derived(new Set(this.namespaces));
	// Of those, where the caller may change VMs: what writes a VM (the VM and
	// template forms, adopting, restoring) reads this set, through canAuthorVMs
	// or here.
	readonly vmNamespaces = $derived(
		this.projects
			.filter((p) => p.repo)
			.flatMap((p) => p.namespaces.filter(vmAuthorable).map((n) => n.namespace)),
	);
	readonly #vmSet = $derived(new Set(this.vmNamespaces));
	// The verdict behind each namespace, for saying why an action is not offered.
	readonly #authoring = $derived(
		new Map<string, Authoring>(
			this.projects.flatMap((p) =>
				p.namespaces.map((n): [string, Authoring] => [
					n.namespace,
					!p.repo || n.canAuthor === false ? 'no' : n.canAuthor === true ? 'yes' : 'unknown',
				]),
			),
		),
	);
	// Some namespace's review failed this frame, so what is offered may be
	// short of what the caller can do.
	readonly accessUnknown = $derived([...this.#authoring.values()].includes('unknown'));
	// Projects with a backing repo - the ones with commit history to browse +
	// revert from (the Changes panel's History section).
	readonly repoProjects = $derived(this.projects.filter((p) => p.repo).map((p) => p.name));

	apply(inv: Inventory) {
		this.inventory = share(this.inventory, inv);
		this.error = '';
	}

	// The catalog is fetched once per session: a call with it loaded is a no-op.
	// A transient boot error would leave sizing and storage-class reads on
	// placeholders until reload, so the pull retries with backoff and the
	// dialogs that need the catalog call again on open, which re-pulls after a
	// failure that outlasted the retries. Concurrent callers share one pull.
	// force re-pulls a catalog the caller just added to (a new boot image);
	// the current one stays on screen until the new one lands.
	loadOptions(force = false): Promise<void> {
		if (this.#optionsPull) return this.#optionsPull;
		if (this.options && !this.optionsError && !force) return Promise.resolve();
		const gen = this.#generation;
		const pull = withRetry(() => api.options())
			.then((o) => {
				if (gen !== this.#generation) return;
				this.options = o;
				this.optionsError = '';
			})
			.catch((e) => {
				if (gen !== this.#generation || e instanceof Unauthorized) return;
				this.optionsError = friendlyError(e);
			})
			.finally(() => {
				if (this.#optionsPull === pull) this.#optionsPull = null;
			});
		this.#optionsPull = pull;
		return pull;
	}

	// loadNet pulls the network catalog and the policy plane together; they
	// are fetched out-of-band, not on the frame. A new pull supersedes the one
	// in flight, so a burst of version bumps writes only the newest answer.
	// signal is the caller's lifetime: once it aborts, the retries stop and
	// nothing is written. retries 0 is one attempt, for a person's Retry. The
	// last good planes stay on screen while a pull fails.
	async loadNet(opts: { signal?: AbortSignal; retries?: number } = {}): Promise<void> {
		this.#netPull?.abort();
		const pull = new AbortController();
		this.#netPull = pull;
		opts.signal?.addEventListener('abort', () => pull.abort(), { once: true });
		this.netBusy = true;
		try {
			const [n, p] = await withRetry(() => Promise.all([api.networks(), api.policies()]), {
				retries: opts.retries,
				signal: pull.signal,
			});
			if (pull.signal.aborted) return;
			this.netInv = n;
			this.polInv = p;
			this.netError = '';
		} catch (e) {
			if (!pull.signal.aborted && !(e instanceof Unauthorized)) this.netError = friendlyError(e);
		} finally {
			if (this.#netPull === pull) {
				this.#netPull = null;
				this.netBusy = false;
			}
		}
	}

	reset() {
		this.#netPull?.abort();
		this.#netPull = null;
		this.netBusy = false;
		this.inventory = null;
		this.netInv = null;
		this.polInv = null;
		this.taskFeed = [];
		this.tasksError = '';
		this.alerts = [];
		this.netError = '';
		this.error = '';
		this.stream = 'connecting';
		this.options = null;
		this.optionsError = '';
		this.#optionsPull = null;
		this.#generation++;
	}

	findVM(namespace: string, name: string): VM | null {
		return this.allVMs.find((v) => v.namespace === namespace && v.name === name) ?? null;
	}

	canAuthorVMs(namespace: string): boolean {
		return this.#vmSet.has(namespace);
	}

	canAuthor(namespace: string): boolean {
		return this.#authorSet.has(namespace);
	}

	authoring(namespace: string): Authoring {
		return this.#authoring.get(namespace) ?? 'no';
	}

	// repoRead is repoReadGate over project, asked of namespace when the
	// caller already holds its answer, else of the first one they see.
	repoRead(project: string, namespace = ''): Gate {
		const p = namespace ? null : this.projects.find((x) => x.name === project);
		return repoReadGate(project, namespace || (p ? projectNS(p) : ''));
	}

	// Revert reads the whole repo and rewrites any file of it, so the backend
	// wants that read and authoring rights in every namespace of the project.
	canRevert(project: string): boolean {
		if (!this.repoRead(project).ok) return false;
		if (project === 'platform') return true;
		const p = this.projects.find((x) => x.name === project);
		return !!p?.namespaces.length && p.namespaces.every(authorable);
	}

	projectOf(namespace: string): string {
		return (
			this.projects.find((p) => p.namespaces.some((n) => n.namespace === namespace))?.name ?? ''
		);
	}
}

export const inventory = new InventoryStore();
