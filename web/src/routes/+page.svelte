<script lang="ts">
	import { untrack } from 'svelte';
	import { ArrowLeft, Plus, Power, PowerOff, Trash2 } from 'lucide-svelte';
	import {
		api,
		draftsByProject,
		streamInventory,
		Unauthorized,
		type DraftItem,
		type DraftView,
		type Inventory,
		type User,
		type VM
	} from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import { vmNetworkKeys, vmStorageKeys } from '$lib/lenses';
	import ActionMenu from '$lib/components/ActionMenu.svelte';
	import ChangesPanel from '$lib/components/ChangesPanel.svelte';
	import ClusterSummary from '$lib/components/ClusterSummary.svelte';
	import ConfirmDelete from '$lib/components/ConfirmDelete.svelte';
	import ContainerMonitor from '$lib/components/ContainerMonitor.svelte';
	import ContextMenu from '$lib/components/ContextMenu.svelte';
	import GlobalSearch, { type SearchHit } from '$lib/components/GlobalSearch.svelte';
	import InventoryTree from '$lib/components/InventoryTree.svelte';
	import Login from '$lib/components/Login.svelte';
	import NewVMWizard from '$lib/components/NewVMWizard.svelte';
	import Permissions from '$lib/components/Permissions.svelte';
	import QuotaBand from '$lib/components/QuotaBand.svelte';
	import StagedChangesModal from '$lib/components/StagedChangesModal.svelte';
	import TaskDock from '$lib/components/TaskDock.svelte';
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
		if (sc.kind === 'network') return all.filter((v) => vmNetworkKeys(v).includes(sc.network));
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
	let showChanges = $state(false);

	async function refreshDrafts() {
		if (!projectNames.length) {
			drafts = [];
			return;
		}
		try {
			drafts = await draftsByProject(projectNames);
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
		selected = vm;
		detailIntent = {
			id: a.id as 'edit' | 'delete' | 'console' | 'snapshot' | 'clone',
			seq: ++intentSeq
		};
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
				onclick={() => (showChanges = !showChanges)}
				class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-100 hover:bg-slate-700"
			>
				Changes{#if draftCount > 0}<span class="ml-1 rounded-full bg-blue-500 px-1.5 text-white"
						>{draftCount}</span
					>{/if}
			</button>
			<button
				onclick={() => (showWizard = true)}
				disabled={!inventory}
				class="flex items-center gap-1.5 rounded bg-blue-600 px-3 py-1 text-xs font-medium text-white hover:bg-blue-500 disabled:opacity-40"
			>
				<Plus size={14} /> New VM
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
					<div class="p-4 text-center text-xs text-slate-400">No projects visible.</div>
				{:else}
					<InventoryTree
						{inventory}
						{selected}
						{scope}
						staged={stagedByKey}
						onselect={(vm) => (selected = vm)}
						onscope={setScope}
						oncontextvm={openVMContext}
						oncontextcontainer={(c, x, y) => (ctx = { x, y, kind: 'container', ...c })}
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
							{#if scope.kind === 'node' || scope.kind === 'network' || scope.kind === 'storage'}
								{@const label =
									scope.kind === 'node'
										? `Node: ${scope.node}`
										: scope.kind === 'network'
											? `Network: ${scope.network}`
											: `Storage class: ${scope.storageClass}`}
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
											: scope.kind === 'network'
												? 'Network definitions (NADs) are managed by the cluster platform, not dotvirt.'
												: 'Storage classes are managed by the cluster platform, not dotvirt.'}
									</p>
								</section>
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
					projects={repoProjects}
					onclose={() => (showChanges = false)}
					onchanged={refreshDrafts}
				/>
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
				onclose={() => {
					showWizard = false;
					wizardNamespaces = null;
				}}
				onstaged={refreshDrafts}
			/>
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
					<div class="w-48 rounded border border-slate-200 bg-white py-1 text-xs shadow-lg">
						<div class="truncate px-3 py-1 text-[10px] tracking-wide text-slate-400 uppercase">
							{ctx.namespace ?? ctx.project}
						</div>
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
