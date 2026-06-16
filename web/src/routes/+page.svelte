<script lang="ts">
	import { untrack } from 'svelte';
	import { ArrowLeft, FolderPlus, Network, Plus, Power, PowerOff, Trash2, Upload } from 'lucide-svelte';
	import {
		api,
		draftsByProject,
		streamInventory,
		Unauthorized,
		type DraftItem,
		type DraftView,
		type Inventory,
		type NetworkInventory,
		type User,
		type VM
	} from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import { vmNetworkKeys, vmStorageKeys, POD_NETWORK } from '$lib/lenses';
	import { networkByRef, kindLabel } from '$lib/networks';
	import ActionMenu from '$lib/components/ActionMenu.svelte';
	import CatalogPanel from '$lib/components/CatalogPanel.svelte';
	import ChangesPanel from '$lib/components/ChangesPanel.svelte';
	import ClusterSummary from '$lib/components/ClusterSummary.svelte';
	import ConfirmDelete from '$lib/components/ConfirmDelete.svelte';
	import ContainerMonitor from '$lib/components/ContainerMonitor.svelte';
	import ContextMenu from '$lib/components/ContextMenu.svelte';
	import GlobalSearch, { type SearchHit } from '$lib/components/GlobalSearch.svelte';
	import InventoryTree from '$lib/components/InventoryTree.svelte';
	import Login from '$lib/components/Login.svelte';
	import AddUplinkModal from '$lib/components/AddUplinkModal.svelte';
	import NewNamespaceModal from '$lib/components/NewNamespaceModal.svelte';
	import NewProjectModal from '$lib/components/NewProjectModal.svelte';
	import AdoptProjectModal from '$lib/components/AdoptProjectModal.svelte';
	import NewNetworkModal from '$lib/components/NewNetworkModal.svelte';
	import NewVMWizard from '$lib/components/NewVMWizard.svelte';
	import NodeActions from '$lib/components/NodeActions.svelte';
	import Permissions from '$lib/components/Permissions.svelte';
	import QuotaBand from '$lib/components/QuotaBand.svelte';
	import StagedChangesModal from '$lib/components/StagedChangesModal.svelte';
	import TaskDock from '$lib/components/TaskDock.svelte';
	import UploadModal from '$lib/components/UploadModal.svelte';
	import VMDetail from '$lib/components/VMDetail.svelte';
	import VMTable from '$lib/components/VMTable.svelte';

	// vCenter model: the tree is a scope selector, the center pane is the VM grid.
	type Scope =
		| { kind: 'all' }
		| { kind: 'project'; project: string }
		| { kind: 'namespace'; project: string; namespace: string }
		| { kind: 'node'; node: string }
		| { kind: 'network'; network: string }
		| { kind: 'storage'; storageClass: string };
	let scope = $state<Scope>({ kind: 'all' });

	let user = $state<User | null>(null);
	let checkingAuth = $state(true);

	let inventory = $state<Inventory | null>(null);
	let selected = $state<VM | null>(null);
	let error = $state<string>('');
	// The networking read layer (GET /api/networks), fetched once per session: the
	// port-group catalog (passed to the VM detail + Networks lens so raw OVN-K refs
	// render as vCenter port groups) plus the physical fabric (uplinks + node NICs,
	// shown on the Nodes lens for node-readers). Changes rarely; backend caches 60s.
	let netInv = $state<NetworkInventory | null>(null);
	const networkCatalog = $derived(netInv?.networks ?? []);
	const uplinks = $derived(netInv?.uplinks ?? []);
	const physicalAdapters = $derived(netInv?.physicalAdapters ?? []);
	const nmstatePresent = $derived(netInv?.nmstatePresent ?? false);
	// May the caller author platform-tier networking (cluster-scoped CUDN/uplink +
	// namespaces)? Gates the New VLAN / Add Uplink / New Namespace actions and the
	// platform changeset, matching the backend platformScope SSAR gate.
	const canManage = $derived(netInv?.canManage ?? false);
	// The synthetic platform-tier project (matches the backend's platformProjectName);
	// holds the cluster-scoped network + namespace changeset, proposable by authors.
	const PLATFORM_PROJECT = 'platform';

	// Bulk selection in the grid (keys "namespace/name"), the bulk-delete confirm, a
	// transient result toast, and an in-flight guard.
	let picked = $state<Set<string>>(new Set());
	let confirmingBulkDelete = $state(false);
	let toast = $state('');
	let bulkBusy = $state(false);

	// Per-project drafts (for the Changes panel + header badge).
	let drafts = $state<{ project: string; draft: DraftView }[]>([]);
	// Open PRs across the user's projects ride the live inventory stream, so a PR
	// merged anywhere (the git poll sees main move) repaints the dock + Changes pane
	// with no manual refresh — no separate fetch to go stale.
	const proposals = $derived(inventory?.proposals ?? []);

	// Runtime ops the user just triggered, surfaced in the dock's Recent Tasks.
	let recentActions = $state<
		{ verb: string; namespace: string; name: string; ok: boolean; at: number }[]
	>([]);
	function recordAction(a: { verb: string; namespace: string; name: string; ok: boolean }) {
		recentActions = [{ ...a, at: Date.now() }, ...recentActions].slice(0, 8);
	}

	// VMs with an unproposed staged change (this user's draft), keyed "ns/name".
	const stagedByKey = $derived.by(() => {
		const m = new Map<string, DraftItem>();
		for (const { draft } of drafts) for (const it of draft.items) m.set(`${it.namespace}/${it.name}`, it);
		return m;
	});

	// The per-VM staged-changes modal (opened from a Staged badge).
	let stagedTarget = $state<VM | null>(null);
	let stagedBusy = $state(false);
	const stagedTargetItem = $derived(
		stagedTarget ? (stagedByKey.get(`${stagedTarget.namespace}/${stagedTarget.name}`) ?? null) : null
	);
	function openStaged(vm: VM) {
		stagedTarget = vm;
	}
	async function discardStaged() {
		if (!stagedTarget) return;
		stagedBusy = true;
		try {
			await api.unstage(stagedTarget.namespace, stagedTarget.name);
			stagedTarget = null;
			await refreshDrafts();
		} catch (e) {
			if (e instanceof Unauthorized) signedOut();
		} finally {
			stagedBusy = false;
		}
	}
	function reviewStaged() {
		stagedTarget = null;
		showChanges = true;
	}

	// Drop to the login screen on any 401.
	function signedOut() {
		user = null;
		inventory = null;
		selected = null;
		drafts = [];
	}

	async function checkAuth() {
		try {
			user = await api.me();
		} catch {
			user = null;
		} finally {
			checkingAuth = false;
		}
	}

	$effect(() => {
		checkAuth();
	});

	// Flatten all VMs across the 3-level tree (project → namespace → vm).
	const allVMs = (inv: Inventory): VM[] =>
		inv.projects.flatMap((p) => p.namespaces.flatMap((n) => n.vms));

	// VMs in the current tree scope, feeding the center grid. Network/storage
	// membership uses the same key helpers as the tree's lens grouping.
	const scopedVMs = $derived.by(() => {
		if (!inventory) return [];
		const sc = scope; // const preserves TS narrowing into the filter closures
		const all = allVMs(inventory);
		if (sc.kind === 'all') return all;
		if (sc.kind === 'node')
			return all.filter((v) => (v.nodeName || '(unscheduled)') === sc.node);
		if (sc.kind === 'network')
			return all.filter((v) => vmNetworkKeys(v, networkCatalog).includes(sc.network));
		if (sc.kind === 'storage')
			return all.filter((v) => vmStorageKeys(v).includes(sc.storageClass));
		return inventory.projects
			.filter((p) => p.name === sc.project)
			.flatMap((p) =>
				p.namespaces
					.filter((n) => sc.kind !== 'namespace' || n.namespace === sc.namespace)
					.flatMap((n) => n.vms)
			);
	});

	// Container workspace: the All/Project/Namespace/Node levels get the same tabbed
	// workspace (Summary/VMs/Monitor/Configure) a VM does — vCenter's "same tabs at
	// every level".
	let containerTab = $state<'summary' | 'vms' | 'monitor' | 'configure' | 'permissions'>(
		'summary'
	);

	// Projects shown on the container Configure tab (the scoped one, or all).
	const cfgProjects = $derived.by(() => {
		if (!inventory) return [];
		const sc = scope;
		if (sc.kind === 'project' || sc.kind === 'namespace')
			return inventory.projects.filter((p) => p.name === sc.project);
		if (sc.kind === 'node' || sc.kind === 'network' || sc.kind === 'storage') return [];
		return inventory.projects;
	});
	// The metrics-backend scope. Network/storage lenses are navigation groupings,
	// not metrics boundaries — their Summary/Monitor aggregate the whole
	// inventory, like 'all'.
	const containerScope = $derived(
		scope.kind === 'project'
			? { project: scope.project }
			: scope.kind === 'namespace'
				? { project: scope.project, namespace: scope.namespace }
				: scope.kind === 'node'
					? { node: scope.node }
					: {}
	);
	const scopedNamespaces = $derived([...new Set(scopedVMs.map((v) => v.namespace))]);

	function setScope(s: Scope) {
		scope = s;
		selected = null;
	}

	function applyInventory(inv: Inventory) {
		inventory = inv;
		error = '';
		if (selected) {
			const still = allVMs(inv).find(
				(v) => v.namespace === selected!.namespace && v.name === selected!.name
			);
			selected = still ?? null;
		}
	}

	// Live subscription, established once signed in. The cookie rides the handshake;
	// a 401 on the upgrade (expired session) drops us back to login.
	$effect(() => {
		if (!user) return;
		inventory = null;
		const stop = streamInventory(applyInventory, signedOut);
		return stop;
	});

	// The port-group catalog: fetched once on sign-in. A failure (e.g. the OVN-K
	// CRDs absent) leaves it empty — NICs then fall back to their raw refs.
	$effect(() => {
		if (!user) return;
		api
			.networks()
			.then((n) => (netInv = n))
			.catch((e) => {
				if (e instanceof Unauthorized) signedOut();
			});
	});

	const projectNames = $derived(inventory ? inventory.projects.map((p) => p.name) : []);
	// A stable primitive key for the SET of project names: $derived arrays are a new
	// reference every inventory frame, which would re-fire the drafts effect on every
	// VM state change. Keying the effect on this string fires it only when the set
	// actually changes.
	const projectKey = $derived([...projectNames].sort().join('\0'));
	const vmCount = $derived(inventory ? allVMs(inventory).length : 0);
	const draftCount = $derived(drafts.reduce((n, d) => n + d.draft.count, 0));
	// Namespaces a VM can be created in: those in projects that have a repo (no
	// point staging into a project with no backing repo).
	const namespaces = $derived(
		inventory
			? inventory.projects
					.filter((p) => p.repo)
					.flatMap((p) => p.namespaces.map((n) => n.namespace))
			: []
	);
	// Projects with a backing repo — the ones that have commit history to browse +
	// revert from, passed to the Changes panel's History section.
	const repoProjects = $derived(
		inventory ? inventory.projects.filter((p) => p.repo).map((p) => p.name) : []
	);

	let showWizard = $state(false);
	let showNetworkWizard = $state(false);
	let showUplinkWizard = $state(false);
	let showNamespaceWizard = $state(false);
	let namespaceWizardProject = $state<string | null>(null);
	let showProjectWizard = $state(false);
	// "Attach repo" target: the labeled-but-repoless project being adopted into git.
	let adoptProjectTarget = $state<{ project: string; namespaces: string[] } | null>(null);
	let showUpload = $state(false);
	let showChanges = $state(false);
	// The catalog browser shares the right-panel slot with Changes (one at a time).
	let showCatalog = $state(false);

	async function refreshDrafts() {
		// Platform authors also carry a platform-tier draft (cluster-scoped network +
		// namespace changes); draftsByProject drops it for non-authors (403 → skipped).
		const names = canManage ? [...projectNames, PLATFORM_PROJECT] : projectNames;
		if (!names.length) {
			drafts = [];
			return;
		}
		try {
			drafts = await draftsByProject(names);
		} catch (e) {
			if (e instanceof Unauthorized) signedOut();
		}
	}

	// Recompute the draft summary only when the SET of projects changes: depend on
	// the stable key, and read the project list via untrack so the effect doesn't
	// also subscribe to the per-frame array reference (which would re-fire on every
	// VM state change). Staging actions call refreshDrafts directly.
	$effect(() => {
		projectKey; // the one tracked dependency
		untrack(() => {
			if (user && projectNames.length) refreshDrafts();
		});
	});

	// --- bulk actions over the grid selection ---

	// The VM objects currently picked (resolve keys against the live inventory).
	const pickedVMs = $derived(
		inventory ? allVMs(inventory).filter((vm) => picked.has(`${vm.namespace}/${vm.name}`)) : []
	);

	let toastTimer: ReturnType<typeof setTimeout> | undefined;
	function showToast(msg: string) {
		toast = msg;
		clearTimeout(toastTimer);
		toastTimer = setTimeout(() => (toast = ''), 5000);
	}

	// Run one staging call per VM in parallel, tallying outcomes. `skip` predicate
	// filters no-ops client-side (e.g. power already in target state); the rest are
	// staged, and any per-VM failure (e.g. a project the user can't edit) folds into
	// the skipped count rather than aborting the batch.
	async function runBulk(
		vms: VM[],
		stage: (vm: VM) => Promise<unknown>,
		skip: (vm: VM) => boolean,
		verb: string
	) {
		if (bulkBusy) return;
		bulkBusy = true;
		try {
			const actionable = vms.filter((vm) => !skip(vm));
			const skipped = vms.length - actionable.length;
			const results = await Promise.allSettled(actionable.map((vm) => stage(vm)));
			if (results.some((r) => r.status === 'rejected' && r.reason instanceof Unauthorized)) {
				signedOut();
				return;
			}
			const failed = results.filter((r) => r.status === 'rejected').length;
			const staged = results.length - failed;
			await refreshDrafts();
			picked = new Set();
			const extra = [
				skipped ? `${skipped} skipped` : '',
				failed ? `${failed} failed` : ''
			]
				.filter(Boolean)
				.join(', ');
			showToast(`${verb} ${staged} of ${vms.length}${extra ? ` (${extra})` : ''}.`);
		} finally {
			bulkBusy = false;
		}
	}

	function bulkPower(target: 'On' | 'Off') {
		runBulk(
			pickedVMs,
			(vm) => api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, power: target }),
			// Already in target state, or not in git (cluster-only) → no-op.
			(vm) => vm.power === target || !vm.sourceFile,
			`Powered ${target.toLowerCase()}: staged`
		);
	}

	async function bulkDelete() {
		confirmingBulkDelete = false;
		await runBulk(
			pickedVMs,
			(vm) => api.stageDelete(vm.namespace, vm.name),
			(vm) => !vm.sourceFile, // not in git → nothing to stage a removal of
			'Deletion staged for'
		);
	}

	async function logout() {
		try {
			await api.logout();
		} catch {
			/* ignore */
		}
		signedOut();
	}

	// Resolve a task's VM (ns/name) against the live inventory and open its detail.
	function selectByKey(namespace: string, name: string) {
		if (!inventory) return;
		selected = allVMs(inventory).find((v) => v.namespace === namespace && v.name === name) ?? null;
	}

	// Right-click context menus — vCenter's signature interaction. One state for
	// all three variants: a VM row (renders the action registry), a row inside a
	// multi-selection (bulk verbs), or a project/namespace row (container verbs).
	type CtxState =
		| { x: number; y: number; kind: 'vm'; vm: VM }
		| { x: number; y: number; kind: 'bulk' }
		| {
				x: number;
				y: number;
				kind: 'container';
				project: string;
				repo?: string;
				namespace?: string;
				namespaces: string[];
		  };
	let ctx = $state<CtxState | null>(null);

	// Host actions picked from a context menu open the VM detail with a one-shot
	// intent (which modal/tab to show); seq re-fires repeats of the same intent.
	let detailIntent = $state<{
		id: 'edit' | 'delete' | 'console' | 'snapshot' | 'clone';
		seq: number;
	} | null>(null);
	let intentSeq = 0;

	function openVMContext(vm: VM, x: number, y: number) {
		// Right-clicking inside a multi-selection acts on the whole selection.
		ctx =
			picked.size > 1 && picked.has(`${vm.namespace}/${vm.name}`)
				? { x, y, kind: 'bulk' }
				: { x, y, kind: 'vm', vm };
	}

	async function onCtxPick(a: VMAction) {
		if (ctx?.kind !== 'vm') return;
		const vm = ctx.vm;
		ctx = null;
		if (a.kind === 'runtime' && a.run) {
			const verb = a.verb ?? a.label;
			try {
				await a.run(vm);
				recordAction({ verb, namespace: vm.namespace, name: vm.name, ok: true });
				showToast(`${verb} requested for ${vm.name}.`);
			} catch (e) {
				if (e instanceof Unauthorized) return signedOut();
				recordAction({ verb, namespace: vm.namespace, name: vm.name, ok: false });
				showToast(String(e));
			}
			return;
		}
		if (a.id === 'manifest') {
			window.open(manifestURL(vm), '_blank');
			return;
		}
		if (a.id === 'adopt') {
			try {
				await api.adopt(vm.namespace, vm.name);
				await refreshDrafts();
				showToast(`${vm.name} staged into Changes — open a PR to adopt it into git.`);
			} catch (e) {
				if (e instanceof Unauthorized) return signedOut();
				showToast(String(e));
			}
			return;
		}
		selected = vm;
		detailIntent = {
			id: a.id as 'edit' | 'delete' | 'console' | 'snapshot' | 'clone',
			seq: ++intentSeq
		};
	}

	// Untracked (NotTracked) VMs in the given namespaces — the rows a bulk adopt acts
	// on. Drives both the "Adopt N untracked" label and which namespaces to call.
	function untrackedVMs(namespaces: string[]): VM[] {
		const want = new Set(namespaces);
		const out: VM[] = [];
		for (const p of inventory?.projects ?? [])
			for (const ns of p.namespaces)
				if (want.has(ns.namespace)) out.push(...ns.vms.filter((v) => v.sync === 'NotTracked'));
		return out;
	}

	// Bulk-adopt every untracked VM under a container into one draft. Only namespaces
	// that actually have untracked VMs are called (AdoptNamespace 400s on an empty one).
	async function bulkAdoptUntracked(namespaces: string[]) {
		const want = new Set(untrackedVMs(namespaces).map((v) => v.namespace));
		try {
			for (const ns of want) await api.adoptNamespace(ns);
			showToast('Untracked VMs staged into Changes — open a PR to adopt them into git.');
		} catch (e) {
			if (e instanceof Unauthorized) return signedOut();
			showToast(String(e));
		} finally {
			// Reflect whatever got staged before any failure — a mid-loop error still
			// leaves the earlier namespaces' adopts in the draft.
			await refreshDrafts();
		}
	}

	// "New VM here" restricts the wizard to the right-clicked container's
	// namespaces; null = the global New VM button (all creatable namespaces).
	let wizardNamespaces = $state<string[] | null>(null);

	// Global search: a hit either opens a VM or re-scopes the tree.
	let search = $state<GlobalSearch | null>(null);
	function onSearchPick(hit: SearchHit) {
		switch (hit.kind) {
			case 'vm':
				selected = hit.vm;
				break;
			case 'project':
				setScope({ kind: 'project', project: hit.project });
				break;
			case 'namespace':
				setScope({ kind: 'namespace', project: hit.project, namespace: hit.namespace });
				break;
			case 'node':
				setScope({ kind: 'node', node: hit.node });
				break;
		}
	}
</script>

{#if checkingAuth}
	<div class="flex h-screen items-center justify-center text-sm text-slate-400">Loading…</div>
{:else if !user}
	<Login onlogin={(u) => (user = u)} />
{:else}
	<div class="flex h-screen flex-col">
		<header
			class="flex items-center gap-3 border-b border-slate-300 bg-slate-800 px-4 py-2 text-white"
		>
			<span class="font-semibold">dotvirt</span>

			<GlobalSearch bind:this={search} {inventory} onpick={onSearchPick} />

			<button
				onclick={() => {
					showChanges = !showChanges;
					if (showChanges) showCatalog = false;
				}}
				class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700"
			>
				Changes{#if draftCount > 0}<span class="ml-1 rounded-full bg-blue-500 px-1.5 text-white"
						>{draftCount}</span
					>{/if}
			</button>
			<button
				onclick={() => {
					showCatalog = !showCatalog;
					if (showCatalog) showChanges = false;
				}}
				title="Browse the cluster's images, instance types, preferences, networks and storage classes"
				class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700"
			>
				Catalog
			</button>
			<button
				onclick={() => (showProjectWizard = true)}
				disabled={!canManage}
				title={canManage
					? 'Create a new tenant project (repo + first namespace)'
					: 'Requires platform authoring permission'}
				class="flex items-center gap-1.5 rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700 disabled:opacity-40"
			>
				<FolderPlus size={14} /> New Project
			</button>
			<button
				onclick={() => (showWizard = true)}
				disabled={!inventory}
				class="flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1 text-xs font-medium text-white hover:bg-blue-500 disabled:opacity-40"
			>
				<Plus size={14} /> New VM
			</button>
			<button
				onclick={() => (showNetworkWizard = true)}
				disabled={!namespaces.length}
				title="Create a Distributed Port Group (an internal Layer 2 network) for a project"
				class="flex items-center gap-1.5 rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700 disabled:opacity-40"
			>
				<Network size={14} /> New Network
			</button>
			<button
				onclick={() => (showUpload = true)}
				disabled={!namespaces.length}
				title="Upload a disk image (qcow2/raw/iso) as a bootable DataVolume"
				class="flex items-center gap-1.5 rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700 disabled:opacity-40"
			>
				<Upload size={14} /> Upload
			</button>
			<div class="text-xs text-slate-400">{vmCount} VMs</div>
			<span class="text-slate-600">|</span>
			<span class="text-xs text-slate-300" title={user.groups.join(', ')}>{user.username}</span>
			<button onclick={logout} class="text-xs text-slate-400 hover:text-white">Sign out</button>
		</header>

		{#if error}
			<div
				class="flex items-start gap-2 border-b border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700"
			>
				<span class="font-medium">Error:</span>
				<span class="font-mono text-xs break-all">{error}</span>
			</div>
		{/if}

		{#if inventory?.warnings?.length}
			<div
				class="flex items-start gap-2 border-b border-amber-200 bg-amber-50 px-4 py-2 text-sm text-amber-800"
			>
				<span class="font-medium">⚠</span>
				<span>{inventory.warnings.join('; ')}</span>
			</div>
		{/if}

		<div class="flex min-h-0 flex-1">
			<aside class="w-72 overflow-y-auto border-r border-slate-300 bg-white">
				{#if !inventory}
					<div class="space-y-2 p-3">
						{#each Array(5) as _, i (i)}
							<div class="h-5 animate-pulse rounded bg-slate-100"></div>
						{/each}
					</div>
				{:else if inventory.projects.length === 0}
					<div class="space-y-3 p-6 text-center">
						<p class="text-xs text-slate-400">No projects visible.</p>
						{#if canManage}
							<button
								onclick={() => (showProjectWizard = true)}
								class="inline-flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-500"
							>
								<FolderPlus size={14} /> Create your first project
							</button>
						{/if}
					</div>
				{:else}
					<InventoryTree
						{inventory}
						{selected}
						{scope}
						networks={networkCatalog}
						staged={stagedByKey}
						onselect={(vm) => (selected = vm)}
						onscope={setScope}
						oncontextvm={openVMContext}
						oncontextcontainer={(c, x, y) => (ctx = { x, y, kind: 'container', ...c })}
						{canManage}
						onattachrepo={(project, namespaces) => (adoptProjectTarget = { project, namespaces })}
					/>
				{/if}
			</aside>
			<main class="flex min-w-0 flex-1 flex-col overflow-hidden bg-white">
				{#if selected}
					<div
						class="flex items-center gap-2 border-b border-slate-200 px-4 py-1.5 text-xs text-slate-500"
					>
						<button
							onclick={() => (selected = null)}
							class="flex items-center gap-1 text-blue-600 hover:underline"
						>
							<ArrowLeft size={13} /> All VMs
						</button>
						<span class="text-slate-300">/</span>
						<span>{selected.namespace}</span>
						<span class="text-slate-300">/</span>
						<span class="font-medium text-slate-700">{selected.name}</span>
					</div>
					<div class="min-h-0 flex-1 overflow-y-auto">
						<VMDetail
							vm={selected}
							onstaged={refreshDrafts}
							onaction={recordAction}
							stagedItem={selected
								? (stagedByKey.get(`${selected.namespace}/${selected.name}`) ?? null)
								: null}
							onstagedopen={() => selected && openStaged(selected)}
							onsearchlabel={(k, v) => search?.searchFor(`label:${k}=${v}`)}
							networks={networkCatalog}
							intent={detailIntent}
						/>
					</div>
				{:else}
					<!-- Container workspace: breadcrumb + the same Summary/VMs/Monitor tabs
					     vCenter gives every inventory level. -->
					<div
						class="flex items-center gap-2 border-b border-slate-200 px-4 py-1.5 text-xs text-slate-500"
					>
						<button onclick={() => setScope({ kind: 'all' })} class="text-blue-600 hover:underline"
							>All VMs</button
						>
						{#if scope.kind === 'project' || scope.kind === 'namespace'}
							{@const proj = scope.project}
							<span class="text-slate-300">/</span>
							<button
								onclick={() => setScope({ kind: 'project', project: proj })}
								class="hover:underline {scope.kind === 'project' ? 'font-medium text-slate-700' : ''}"
								>{proj}</button
							>
						{/if}
						{#if scope.kind === 'namespace'}
							<span class="text-slate-300">/</span>
							<span class="font-medium text-slate-700">{scope.namespace}</span>
						{/if}
						{#if scope.kind === 'node'}
							<span class="text-slate-300">/</span>
							<span class="font-medium text-slate-700">Node: {scope.node}</span>
						{/if}
						{#if scope.kind === 'network'}
							<span class="text-slate-300">/</span>
							<span class="font-medium text-slate-700">Network: {scope.network}</span>
						{/if}
						{#if scope.kind === 'storage'}
							<span class="text-slate-300">/</span>
							<span class="font-medium text-slate-700">Storage: {scope.storageClass}</span>
						{/if}
					</div>

					<nav class="flex gap-1 border-b border-slate-200 px-4 text-sm">
						{#each [['summary', 'Summary'], ['vms', 'VMs'], ['monitor', 'Monitor'], ['configure', 'Configure'], ['permissions', 'Permissions']] as const as [t, label] (t)}
							<button
								class="border-b-2 px-3 py-1.5 {containerTab === t
									? 'border-blue-600 text-blue-700'
									: 'border-transparent text-slate-500 hover:text-slate-700'}"
								onclick={() => (containerTab = t)}
							>
								{label}
							</button>
						{/each}
					</nav>

					{#if containerTab === 'summary'}
						<div class="min-h-0 flex-1 overflow-y-auto">
							<ClusterSummary scope={containerScope} onselect={selectByKey} />
						</div>
					{:else if containerTab === 'monitor'}
						<div class="min-h-0 flex-1 overflow-y-auto">
							<ContainerMonitor
							namespaces={scopedNamespaces}
							scope={containerScope}
							onselect={selectByKey}
						/>
						</div>
					{:else if containerTab === 'permissions'}
						<div class="min-h-0 flex-1 overflow-y-auto p-4">
							<Permissions namespaces={scopedNamespaces} />
						</div>
					{:else if containerTab === 'configure'}
						<!-- Read-only container settings: what backs each project. dotvirt owns
						     nothing here — projects are namespace labels, config is the repo. -->
						<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
							{#if scope.kind === 'network'}
								{@const pg = networkByRef(scope.network, networkCatalog)}
								<section class="max-w-2xl rounded border border-slate-200">
									<h3
										class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
									>
										{pg ? pg.name : scope.network}
									</h3>
									<dl class="divide-y divide-slate-100 text-[13px]">
										<div class="flex justify-between gap-3 px-3 py-1.5">
											<dt class="shrink-0 text-slate-500">Type</dt>
											<dd class="text-slate-800">
												{pg
													? kindLabel(pg.kind)
													: scope.network === POD_NETWORK
														? 'Pod network (cluster default)'
														: '—'}
											</dd>
										</div>
										{#if pg}
											<div class="flex justify-between gap-3 px-3 py-1.5">
												<dt class="shrink-0 text-slate-500">Scope</dt>
												<dd class="min-w-0 truncate text-right text-slate-800">
													{pg.scope === 'shared' ? 'Shared · all projects' : `Project · ${pg.namespace}`}
												</dd>
											</div>
											{#if pg.vlan}
												<div class="flex justify-between gap-3 px-3 py-1.5">
													<dt class="shrink-0 text-slate-500">VLAN</dt>
													<dd class="text-slate-800">{pg.vlan}</dd>
												</div>
											{/if}
											{#if pg.uplink}
												<div class="flex justify-between gap-3 px-3 py-1.5">
													<dt class="shrink-0 text-slate-500">Uplink</dt>
													<dd class="text-slate-800">{pg.uplink}</dd>
												</div>
											{/if}
											{#if pg.subnets?.length}
												<div class="flex justify-between gap-3 px-3 py-1.5">
													<dt class="shrink-0 text-slate-500">Subnets</dt>
													<dd class="min-w-0 truncate text-right text-slate-800">{pg.subnets.join(', ')}</dd>
												</div>
											{/if}
											<div class="flex justify-between gap-3 px-3 py-1.5">
												<dt class="shrink-0 text-slate-500">Backing</dt>
												<dd class="min-w-0 truncate text-right text-slate-500">{pg.backing}</dd>
											</div>
										{/if}
										<div class="flex justify-between gap-3 px-3 py-1.5">
											<dt class="shrink-0 text-slate-500">VMs attached</dt>
											<dd class="text-slate-800">{scopedVMs.length}</dd>
										</div>
									</dl>
								</section>
							{:else if scope.kind === 'node' || scope.kind === 'storage'}
								{@const label =
									scope.kind === 'node' ? `Node: ${scope.node}` : `Storage class: ${scope.storageClass}`}
								<section class="max-w-2xl rounded border border-slate-200">
									<h3
										class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
									>
										{label}
									</h3>
									<dl class="divide-y divide-slate-100 text-[13px]">
										<div class="flex justify-between gap-3 px-3 py-1.5">
											<dt class="text-slate-500">
												{scope.kind === 'node' ? 'VMs placed here' : 'VMs attached'}
											</dt>
											<dd class="text-slate-800">{scopedVMs.length}</dd>
										</div>
									</dl>
									<p class="border-t border-slate-100 px-3 py-2 text-xs text-slate-400">
										{scope.kind === 'node'
											? 'Node configuration is managed by the cluster platform, not dotvirt.'
											: 'Storage classes are managed by the cluster platform, not dotvirt.'}
									</p>
								</section>
								{#if scope.kind === 'node'}
									{@const nodeName = scope.node}
									<!-- Node maintenance-lite: cordon/uncordon + evacuate (shown only
									     when the caller's token may patch nodes). -->
									<NodeActions node={scope.node} vms={scopedVMs} onaction={recordAction} />
									{#if uplinks.length}
										<section class="max-w-2xl rounded border border-slate-200">
											<div class="flex items-center justify-between border-b border-slate-200 bg-slate-50 px-3 py-1.5">
												<h3 class="text-xs font-semibold tracking-wide text-slate-500 uppercase">Uplinks</h3>
												<button
													onclick={() => (showUplinkWizard = true)}
													disabled={!canManage}
													title={canManage ? '' : 'Requires platform-network authoring permission'}
													class="text-xs text-blue-600 hover:underline disabled:text-slate-300"
													>+ Add uplink</button
												>
											</div>
											<ul class="divide-y divide-slate-100 px-3 text-[13px]">
												{#each uplinks.filter((u) => !u.nodes || u.nodes.includes(nodeName)) as u (u.name)}
													<li class="flex items-baseline justify-between gap-3 py-1.5">
														<span class="text-slate-800">{u.name}{u.builtin ? ' · default' : ''}</span>
														<span class="text-slate-400"
															>{u.bridge} · {u.nodeCount} node{u.nodeCount === 1 ? '' : 's'}</span>
													</li>
												{/each}
											</ul>
										</section>
									{/if}
									<section class="max-w-2xl rounded border border-slate-200">
										<h3 class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">Physical adapters</h3>
										{#if !nmstatePresent}
											<p class="px-3 py-3 text-xs text-slate-400">
												Install the NMState operator instance to discover physical adapters.
											</p>
										{:else}
											{@const nics = physicalAdapters.filter((a) => a.node === nodeName)}
											{#if nics.length}
												<ul class="divide-y divide-slate-100 px-3 text-[13px]">
													{#each nics as a (a.name)}
														<li class="flex items-baseline justify-between gap-3 py-1.5">
															<span class="text-slate-800">{a.name}</span>
															<span class="flex items-center gap-3 text-right text-slate-400">
																<span>{a.role}</span>
																<span>{a.state}{a.mtu ? ` · MTU ${a.mtu}` : ''}</span>
															</span>
														</li>
													{/each}
												</ul>
											{:else}
												<p class="px-3 py-3 text-xs text-slate-400">No physical adapters reported.</p>
											{/if}
										{/if}
									</section>
								{/if}
							{:else}
								{#each cfgProjects as p (p.name)}
									<section class="max-w-2xl rounded border border-slate-200">
										<h3
											class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
										>
											Project: {p.name}
										</h3>
										<dl class="divide-y divide-slate-100 text-[13px]">
											<div class="flex justify-between gap-3 px-3 py-1.5">
												<dt class="shrink-0 text-slate-500">Repository</dt>
												<dd class="min-w-0 truncate text-right">
													{#if p.repo}
														<a
															href={p.repo}
															target="_blank"
															class="font-mono text-xs text-blue-600 hover:underline">{p.repo}</a
														>
													{:else}
														<span class="text-slate-400">— not configured</span>
													{/if}
												</dd>
											</div>
											<div class="flex justify-between gap-3 px-3 py-1.5">
												<dt class="shrink-0 text-slate-500">Namespaces</dt>
												<dd class="min-w-0 text-right">
													{#each p.namespaces as n (n.namespace)}
														<span
															class="ml-1 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600"
															>{n.namespace} · {n.vms.length} VMs</span
														>
													{/each}
												</dd>
											</div>
										</dl>
										{#if p.error}
											<p
												class="border-t border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700"
											>
												{p.error}
											</p>
										{/if}
										<!-- Quota-aware capacity: the project's ResourceQuotas. -->
										<div class="border-t border-slate-100 px-3 py-2">
											<QuotaBand scope={{ project: p.name }} showEmpty />
										</div>
									</section>
								{/each}
							{/if}
						</div>
					{:else}
						{#if picked.size > 0}
							<div
								class="flex items-center gap-2 border-b border-slate-200 bg-blue-50 px-4 py-1.5 text-sm"
							>
								<span class="font-medium text-slate-700">{picked.size} selected</span>
							<span class="text-slate-300">|</span>
							<button
								onclick={() => bulkPower('On')}
								disabled={bulkBusy}
								class="flex items-center gap-1.5 rounded border border-slate-300 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
							>
								<Power size={13} class="text-green-600" /> Power On
							</button>
							<button
								onclick={() => bulkPower('Off')}
								disabled={bulkBusy}
								class="flex items-center gap-1.5 rounded border border-slate-300 bg-white px-2.5 py-1 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
							>
								<PowerOff size={13} class="text-slate-500" /> Power Off
							</button>
							<button
								onclick={() => {
									confirmingBulkDelete = true;
								}}
								disabled={bulkBusy}
								class="flex items-center gap-1.5 rounded border border-red-300 bg-white px-2.5 py-1 text-xs font-medium text-red-700 hover:bg-red-50 disabled:opacity-50"
							>
								<Trash2 size={13} /> Delete
							</button>
							<button
								onclick={() => (picked = new Set())}
								class="ml-auto text-xs text-slate-500 hover:text-slate-700"
							>
								Clear
							</button>
						</div>
					{/if}
					<VMTable
						vms={scopedVMs}
						bind:selected={picked}
						staged={stagedByKey}
						onselect={(vm) => (selected = vm)}
						onstagedopen={openStaged}
						oncontextvm={openVMContext}
					/>
				{/if}
				{/if}
			</main>

			{#if showChanges}
				<ChangesPanel
					{drafts}
					{proposals}
					projects={canManage ? [...repoProjects, PLATFORM_PROJECT] : repoProjects}
					onclose={() => (showChanges = false)}
					onchanged={refreshDrafts}
				/>
			{/if}

			{#if showCatalog}
				<CatalogPanel onclose={() => (showCatalog = false)} />
			{/if}
		</div>

		<TaskDock
			{drafts}
			{proposals}
			actions={recentActions}
			{inventory}
			username={user.username}
			onselect={selectByKey}
			onrefresh={refreshDrafts}
		/>

		{#if showWizard}
			<NewVMWizard
				namespaces={wizardNamespaces ?? namespaces}
				networks={networkCatalog}
				onclose={() => {
					showWizard = false;
					wizardNamespaces = null;
				}}
				onstaged={refreshDrafts}
			/>
		{/if}

		{#if showNetworkWizard}
			<NewNetworkModal
				{namespaces}
				projects={repoProjects}
				{uplinks}
				{canManage}
				onAddUplink={() => (showUplinkWizard = true)}
				onclose={() => (showNetworkWizard = false)}
				onstaged={refreshDrafts}
			/>
		{/if}

		{#if showUplinkWizard}
			<AddUplinkModal
				adapters={physicalAdapters}
				onclose={() => (showUplinkWizard = false)}
				onstaged={refreshDrafts}
			/>
		{/if}

		{#if showNamespaceWizard}
			<NewNamespaceModal
				projects={repoProjects}
				project={namespaceWizardProject ?? undefined}
				onclose={() => {
					showNamespaceWizard = false;
					namespaceWizardProject = null;
				}}
				onstaged={refreshDrafts}
			/>
		{/if}

		{#if showProjectWizard}
			<NewProjectModal onclose={() => (showProjectWizard = false)} onstaged={refreshDrafts} />
		{/if}

		{#if adoptProjectTarget}
			<AdoptProjectModal
				project={adoptProjectTarget.project}
				namespaces={adoptProjectTarget.namespaces}
				onclose={() => (adoptProjectTarget = null)}
				onstaged={refreshDrafts}
			/>
		{/if}

		{#if showUpload}
			<UploadModal {namespaces} onclose={() => (showUpload = false)} />
		{/if}

		{#if ctx}
			<ContextMenu x={ctx.x} y={ctx.y} onclose={() => (ctx = null)}>
				{#if ctx.kind === 'vm'}
					<ActionMenu vm={ctx.vm} onpick={onCtxPick} />
				{:else if ctx.kind === 'bulk'}
					<div class="w-48 rounded border border-slate-200 bg-white py-1 text-xs shadow-lg">
						<div class="px-3 py-1 text-[10px] tracking-wide text-slate-400 uppercase">
							{picked.size} VMs selected
						</div>
						<button
							onclick={() => {
								ctx = null;
								bulkPower('On');
							}}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
							>Power On (staged)</button
						>
						<button
							onclick={() => {
								ctx = null;
								bulkPower('Off');
							}}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
							>Power Off (staged)</button
						>
						<div class="my-1 border-t border-slate-100"></div>
						<button
							onclick={() => {
								ctx = null;
								confirmingBulkDelete = true;
							}}
							class="block w-full px-3 py-1.5 text-left text-red-700 hover:bg-red-50"
							>Delete {picked.size} VMs…</button
						>
						<div class="my-1 border-t border-slate-100"></div>
						<button
							onclick={() => {
								ctx = null;
								picked = new Set();
							}}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
							>Clear selection</button
						>
					</div>
				{:else}
					{@const untracked = untrackedVMs(ctx.namespaces)}
					<div class="w-48 rounded border border-slate-200 bg-white py-1 text-xs shadow-lg">
						<div class="truncate px-3 py-1 text-[10px] tracking-wide text-slate-400 uppercase">
							{ctx.namespace ?? ctx.project}
						</div>
						{#if !ctx.repo && canManage}
							<button
								onclick={() => {
									adoptProjectTarget =
										ctx && ctx.kind === 'container'
											? { project: ctx.project, namespaces: ctx.namespaces }
											: null;
									ctx = null;
								}}
								title="Create a repo for this project and bring it under GitOps"
								class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
								>Attach repo…</button
							>
							<div class="my-1 border-t border-slate-100"></div>
						{/if}
						{#if ctx.repo && untracked.length}
							<button
								onclick={() => {
									const ns = ctx && ctx.kind === 'container' ? ctx.namespaces : [];
									ctx = null;
									bulkAdoptUntracked(ns);
								}}
								title="Stage every untracked VM here into one PR"
								class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
								>Adopt {untracked.length} untracked…</button
							>
						{/if}
						<button
							onclick={() => {
								wizardNamespaces = ctx && ctx.kind === 'container' ? ctx.namespaces : null;
								ctx = null;
								showWizard = true;
							}}
							disabled={!ctx.repo}
							title={ctx.repo ? '' : 'Project has no backing repo'}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50 disabled:cursor-not-allowed disabled:text-slate-300"
							>New VM here…</button
						>
						{#if canManage}
							<button
								onclick={() => {
									namespaceWizardProject = ctx && ctx.kind === 'container' ? ctx.project : null;
									ctx = null;
									showNamespaceWizard = true;
								}}
								disabled={!ctx.repo}
								title={ctx.repo ? '' : 'Project has no backing repo'}
								class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50 disabled:cursor-not-allowed disabled:text-slate-300"
								>New Namespace here…</button
							>
						{/if}
						<div class="my-1 border-t border-slate-100"></div>
						<button
							onclick={() => {
								if (ctx?.kind === 'container' && ctx.repo) window.open(ctx.repo, '_blank');
								ctx = null;
							}}
							disabled={!ctx.repo}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50 disabled:cursor-not-allowed disabled:text-slate-300"
							>Open repository ↗</button
						>
						<button
							onclick={() => {
								ctx = null;
								showChanges = true;
							}}
							class="block w-full px-3 py-1.5 text-left text-slate-700 hover:bg-slate-50"
							>Changes &amp; history</button
						>
					</div>
				{/if}
			</ContextMenu>
		{/if}

		{#if confirmingBulkDelete}
			<ConfirmDelete
				title="Delete {pickedVMs.length} VMs"
				confirmWord="delete"
				busy={bulkBusy}
				onconfirm={bulkDelete}
				onclose={() => (confirmingBulkDelete = false)}
			>
				<p class="mb-3">
					This stages removal of the following VMs into <strong>Changes</strong>. They are deleted from
					the cluster only when each project's PR is merged.
				</p>
				<ul class="max-h-40 overflow-y-auto rounded border border-slate-200 text-xs">
					{#each pickedVMs as vm (vm.namespace + '/' + vm.name)}
						<li class="border-b border-slate-100 px-2 py-1 last:border-0">
							<span class="font-medium text-slate-800">{vm.name}</span>
							<span class="text-slate-400">· {vm.namespace}</span>
						</li>
					{/each}
				</ul>
			</ConfirmDelete>
		{/if}

		{#if stagedTarget && stagedTargetItem}
			<StagedChangesModal
				item={stagedTargetItem}
				busy={stagedBusy}
				onclose={() => (stagedTarget = null)}
				ondiscard={discardStaged}
				onreview={reviewStaged}
			/>
		{/if}

		{#if toast}
			<div
				class="fixed bottom-4 left-1/2 z-50 -translate-x-1/2 rounded-md bg-slate-800 px-4 py-2 text-sm text-white shadow-lg"
			>
				{toast}
			</div>
		{/if}
	</div>
{/if}
