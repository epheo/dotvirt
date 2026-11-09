<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { CircleAlert, GitPullRequest } from '@lucide/svelte';
	import { isStaged, toastStageResult } from '#lib/actions.js';
	import { api, Unauthorized, vmKey, vmRef, type DraftView, type VM } from '#lib/api.js';
	import { bulkOutcome } from '#lib/bulk.js';
	import { bytes, plural } from '#lib/format.js';
	import { vmEditGate, vmGate, type Gate } from '#lib/gates.js';
	import { hostState, hostsAttention, vmHeadline, vmStats, type Headline } from '#lib/headline.js';
	import { vmNetworkKeys, vmStorageKeys, type Scope } from '#lib/lenses.js';
	import { metricsGap } from '#lib/metrics.js';
	import { containerTabs, vmHref } from '#lib/nav.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { hostFacts } from '#lib/state/hosts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import AdoptBanner from '#lib/components/AdoptBanner.svelte';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import BulkActionsBar from '#lib/components/BulkActionsBar.svelte';
	import ClusterSummary from '#lib/components/ClusterSummary.svelte';
	import ContainerConfigure from '#lib/components/ContainerConfigure.svelte';
	import ContainerMonitor from '#lib/components/ContainerMonitor.svelte';
	import EffectivePolicyPanel from '#lib/components/EffectivePolicyPanel.svelte';
	import NodeActions from '#lib/components/NodeActions.svelte';
	import RepoBanner from '#lib/components/RepoBanner.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import Permissions from '#lib/components/Permissions.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import VMTable from '#lib/components/VMTable.svelte';
	import Inspector from '#lib/components/Inspector.svelte';
	import BulkActionsMenu from './BulkActionsMenu.svelte';
	import BulkDeleteConfirm from './BulkDeleteConfirm.svelte';
	import HostsRootSummary from './HostsRootSummary.svelte';
	import NodeConfigure from './NodeConfigure.svelte';
	import TemplatesTab from './TemplatesTab.svelte';

	// The container workspace: every inventory level, section roots included,
	// gets the same breadcrumb, header and tab chrome over the grey ground.
	// Roots differ only in what their Summary shows (cluster capacity, the host
	// fleet). The tab SET follows the object kind: compute containers carry the
	// full set, hosts drop Permissions (nodes aren't namespaced).
	let {
		scope,
		section,
		trail,
	}: {
		scope: Scope;
		// The section this workspace serves: a root ('all' scope) picks its
		// Summary and header by it, and every route names it so the chrome
		// can't drift.
		section: 'compute' | 'hosts';
		// A root's trail is the section alone; the tab in view is appended.
		trail: { label: string; href?: string }[];
	} = $props();
	const root = $derived(scope.kind === 'all');

	const tabSet = $derived(containerTabs(scope, section));
	const tab = $derived.by(() => {
		const t = page.url.searchParams.get('tab');
		return tabSet.some((x) => x.id === t) ? t! : 'summary';
	});

	// VMs in the current scope, feeding the grid. Network/storage membership uses
	// the same key helpers as the tree's grouping, so they can never disagree.
	const scopedVMs = $derived.by(() => {
		const sc = scope; // const preserves TS narrowing into the filter closures
		const all = inventory.allVMs;
		if (sc.kind === 'all') return all;
		if (sc.kind === 'node') return all.filter((v) => (v.nodeName || '(unscheduled)') === sc.node);
		if (sc.kind === 'network')
			return all.filter((v) => vmNetworkKeys(v, inventory.networks).includes(sc.network));
		if (sc.kind === 'storage')
			return all.filter((v) =>
				vmStorageKeys(v, inventory.defaultStorageClass).includes(sc.storageClass),
			);
		return inventory.projects
			.filter((p) => p.name === sc.project)
			.flatMap((p) =>
				p.namespaces
					.filter((n) => sc.kind !== 'namespace' || n.namespace === sc.namespace)
					.flatMap((n) => n.vms),
			);
	});

	// Projects shown on the Configure tab (the scoped one, or all of Compute).
	const cfgProjects = $derived.by(() => {
		const sc = scope;
		if (sc.kind === 'project' || sc.kind === 'namespace')
			return inventory.projects.filter((p) => p.name === sc.project);
		return sc.kind === 'all' && section === 'compute' ? inventory.projects : [];
	});
	// The metrics-backend scope. Network/storage lenses are navigation groupings,
	// not metrics boundaries - their Summary/Monitor aggregate the whole
	// inventory, like 'all'.
	const containerScope = $derived(
		scope.kind === 'project'
			? { project: scope.project }
			: scope.kind === 'namespace'
				? { project: scope.project, namespace: scope.namespace }
				: scope.kind === 'node'
					? { node: scope.node }
					: {},
	);
	const scopedNamespaces = $derived([...new Set(scopedVMs.map((v) => v.namespace))]);

	const tabs = $derived(
		tabSet.map((t) => ({ ...t, count: t.id === 'vms' ? scopedVMs.length : undefined })),
	);
	const crumbs = $derived(
		root ? [...trail, { label: tabSet.find((t) => t.id === tab)?.label ?? 'Summary' }] : trail,
	);

	// What the Hosts pages read about their hosts, shared by the header and
	// the Summary so each is asked once per page.
	const node = $derived(scope.kind === 'node' ? scope.node : '');
	const facts = hostFacts({
		on: () => section === 'hosts',
		node: () => node,
		live: () => tab === 'summary',
	});
	const nodes = $derived(facts.nodes.failed ? null : facts.nodes.data);
	const capacity = $derived(
		facts.cap.failed || !facts.cap.data || metricsGap(facts.cap.data.unavailable)
			? null
			: facts.cap.data,
	);
	const nodeInfo = $derived(facts.info.failed ? null : facts.info.data);

	// What the scope's VMs are doing, for the header; the Hosts root counts
	// hosts instead. "In a change" is staged or in an open PR, the same set as
	// the VMs tab's chip.
	const vmHeader = $derived(!(root && section === 'hosts'));
	$effect(() => {
		inventory.proposalsKey;
		if (vmHeader) untrack(() => reviewCache.loadProposals(inventory.proposals));
	});
	const inPRs = $derived(reviewCache.vmPRs(inventory.proposals));
	const issueKeys = $derived(new Set(inventory.issues.map((i) => i.scope)));
	const stats = $derived(
		vmHeader
			? vmStats(scopedVMs, issueKeys, (k) => drafts.stagedByKey.has(k) || inPRs.has(k))
			: null,
	);

	// The header names the scope, says how it stands, and how big it is: a
	// compute container by its VMs, a host by its state, the Hosts root by the
	// hosts that need a person.
	const head = $derived.by((): { title: string; status: Headline | null; context: string } => {
		const sc = scope;
		const vms = plural(scopedVMs.length, 'VM');
		if (sc.kind === 'node') {
			const listed = nodes?.find((n) => n.name === sc.node);
			const flags = {
				ready: listed?.ready,
				unschedulable: nodeInfo?.unschedulable ?? listed?.unschedulable,
				maintenance: nodeInfo?.maintenance ?? listed?.maintenance,
			};
			// Readiness is the node list's alone: without it, only a cordon or
			// maintenance the host's own read reports is said.
			const st = listed || flags.maintenance || flags.unschedulable ? hostState(flags) : null;
			const c = capacity?.nodes.find((n) => n.node === sc.node);
			return {
				title: sc.node,
				status: st && { tone: st.tone, text: st.label },
				context: [
					vms,
					c ? plural(Math.round(c.cpuAllocatable), 'core') : '',
					c ? `${bytes(c.memAllocatable)} memory` : '',
				]
					.filter(Boolean)
					.join(', '),
			};
		}
		if (sc.kind === 'all' && section === 'hosts') {
			const names = new Set((nodes ?? []).map((n) => n.name));
			for (const v of scopedVMs) if (v.nodeName) names.add(v.nodeName);
			return {
				title: 'Hosts',
				status: nodes ? hostsAttention(nodes) : null,
				context: `${plural(names.size, 'host')}, ${vms}`,
			};
		}
		const status = stats && vmHeadline(stats);
		if (sc.kind === 'namespace')
			return { title: sc.namespace, status, context: `${vms} in ${sc.project}` };
		if (sc.kind === 'project') {
			const p = inventory.projects.find((x) => x.name === sc.project);
			return {
				title: sc.project,
				status,
				context: `${vms} in ${plural(p?.namespaces.length ?? 0, 'namespace')}`,
			};
		}
		return {
			title: 'All VMs',
			status,
			context: `${vms} in ${plural(inventory.projects.length, 'project')}`,
		};
	});

	const openVM = (namespace: string, name: string) => goto(vmHref(namespace, name));

	// The side peek: ?peek=ns/name inspects a row in place (replaceState, like
	// tabs - back never walks peek moves). Resolved against the scoped list, so
	// a VM that leaves the scope (or the inventory) closes its peek.
	const peekKey = $derived(page.url.searchParams.get('peek'));
	const peekVM = $derived(peekKey ? (scopedVMs.find((v) => vmKey(v) === peekKey) ?? null) : null);
	function setPeek(vm: VM | null) {
		const u = new URL(page.url.href);
		if (vm) u.searchParams.set('peek', vmKey(vm));
		else u.searchParams.delete('peek');
		goto(u.pathname + u.search, { replace: true, reset: false });
	}

	// --- bulk actions over the grid selection (keys "namespace/name") ---
	// Deliberately independent of the VM action registry: Power isn't a registry
	// action, and these stage batch edits, not runtime ops.

	let picked = $state<Set<string>>(new Set());
	let confirmingBulkDelete = $state(false);
	const bulkOp = action({ toast: ui.toastError });

	// The VM objects currently picked (resolve keys against the live inventory).
	const pickedVMs = $derived(inventory.allVMs.filter((vm) => picked.has(vmKey(vm))));

	// A right-click inside the multi-selection acts on the selection: the bulk
	// menu renders here; the shell renders the single-VM variant.
	let bulkCtx = $state<{ x: number; y: number } | null>(null);
	function contextVM(vm: VM, x: number, y: number) {
		if (picked.size > 1 && picked.has(vmKey(vm))) bulkCtx = { x, y };
		else ui.openVMContext(vm, x, y);
	}

	// Run one staging call per VM in parallel, tallying outcomes. `skip` filters
	// no-ops client-side; a per-VM failure never aborts the batch. The tally
	// reads each answer: a stage that left its VM matching git staged nothing.
	function runBulk(
		vms: VM[],
		stage: (vm: VM) => Promise<DraftView>,
		skip: (vm: VM) => boolean,
		verb: string,
	) {
		if (bulkOp.busy) return;
		return bulkOp.run(async () => {
			const actionable = vms.filter((vm) => !skip(vm));
			const results = await Promise.allSettled(actionable.map((vm) => stage(vm)));
			// allSettled hides a sign-out from the action's own swallow.
			const denied = results.find(
				(r) => r.status === 'rejected' && r.reason instanceof Unauthorized,
			) as PromiseRejectedResult | undefined;
			if (denied) throw denied.reason;
			const { keep, staged, failed, msg } = bulkOutcome(
				verb,
				vms.length,
				actionable,
				results,
				(view, i) => isStaged(view as DraftView, actionable[i]),
			);
			picked = keep;
			// One VM reads like its own page's button.
			if (vms.length === 1 && results[0]?.status === 'fulfilled')
				toastStageResult(results[0].value, actionable[0], msg);
			else if (staged > 0) ui.toastStaged(msg, { kind: failed ? 'error' : 'success' });
			else ui.showToast(msg, { kind: failed ? 'error' : 'success' });
		});
	}

	function bulkPower(target: 'On' | 'Off') {
		runBulk(
			pickedVMs,
			(vm) => api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, power: target }),
			// Already headed there (a staged power change counts), not in git
			// (cluster-only), or not the caller's to change: nothing to stage.
			(vm) =>
				(drafts.stagedByKey.get(vmKey(vm))?.edit?.power ?? vm.power) === target ||
				!vmEditGate(vm).ok,
			`Powered ${target.toLowerCase()}: staged`,
		);
	}

	// A bulk action runs on the picked VMs the caller may act on; it is held
	// back only when none may, with the first one's reason.
	const bulkWhy = (gate: (vm: VM) => Gate) => {
		const gates = pickedVMs.map(gate);
		return gates.some((g) => g.ok) ? '' : (gates[0]?.why ?? '');
	};
	const powerWhy = $derived(bulkWhy(vmEditGate));
	const deleteWhy = $derived(bulkWhy((vm) => vmGate.delete(vm.namespace)));

	async function bulkDelete() {
		confirmingBulkDelete = false;
		await runBulk(
			pickedVMs,
			(vm) => api.stageDelete(vm.namespace, vm.name),
			// Not in git (nothing to remove), or not the caller's to delete.
			(vm) => !vm.sourceFile || !vmGate.delete(vm.namespace).ok,
			'Deletion staged for',
		);
	}
</script>

{#snippet context()}
	<span class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px]">
		<span class="text-ink-faint">{head.context}</span>
		{#if scope.kind === 'node' && stats?.issues}
			<a href="?tab=vms" class="inline-flex items-center gap-1.5 text-danger-ink hover:underline"
				><CircleAlert size={14} />{plural(stats.issues, 'VM')} with issues</a
			>
		{/if}
		{#if stats?.inChange}
			<a href="?tab=vms" class="inline-flex items-center gap-1.5 text-accent-ink hover:underline"
				><GitPullRequest size={14} />{stats.inChange.toLocaleString('en-US')} in a change</a
			>
		{/if}
	</span>
{/snippet}

{#snippet hostActions()}
	{#if nodeInfo}
		<NodeActions
			{node}
			vms={scopedVMs}
			info={nodeInfo}
			onchange={() => Promise.all([facts.info.refresh(), facts.nodes.refresh()])}
		/>
	{/if}
{/snippet}

<div class="flex min-h-0 flex-1 flex-col bg-canvas">
	<div class="flex flex-col gap-2 px-6 pt-5">
		<Breadcrumb trail={crumbs} />
		<PageHeader
			title={head.title}
			status={head.status ?? undefined}
			{context}
			actions={scope.kind === 'node' ? hostActions : undefined}
		/>
		<TabBar {tabs} active={tab} href={(t) => `?tab=${t}`} />
	</div>

	{#if tab === 'vms'}
		<div class="flex min-h-0 flex-1 flex-col px-6 pt-4 pb-7">
			<section
				aria-label="{head.title}, VMs"
				class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-line bg-panel"
			>
				{#if picked.size > 0}
					<BulkActionsBar
						count={picked.size}
						busy={bulkOp.busy}
						{powerWhy}
						{deleteWhy}
						onpower={bulkPower}
						ondelete={() => (confirmingBulkDelete = true)}
						onclear={() => (picked = new Set())}
					/>
				{/if}
				<div class="flex min-h-0 flex-1">
					<div class="min-w-0 flex-1">
						<VMTable
							vms={scopedVMs}
							bind:selected={picked}
							staged={drafts.stagedByKey}
							activeKey={peekVM ? peekKey : null}
							onselect={(vm) => setPeek(peekKey === vmKey(vm) ? null : vm)}
							onstagedopen={(vm) => (ui.modal = { kind: 'staged', vm: vmRef(vm) })}
							oncontextvm={contextVM}
						/>
					</div>
					{#if peekVM}
						<Inspector vm={peekVM} onclose={() => setPeek(null)} />
					{/if}
				</div>
			</section>
		</div>
	{:else}
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div
			class="min-h-0 flex-1 overflow-y-auto px-6 pt-4 pb-7"
			role="region"
			aria-label="{head.title}, {tab}"
			tabindex="0"
		>
			{#if tab === 'summary'}
				{#if root && section === 'hosts'}
					<HostsRootSummary vms={scopedVMs} {facts} />
				{:else}
					{#if scope.kind === 'project' || scope.kind === 'namespace'}
						<!-- The project's standing prompts, as callouts on the ground; the
						     frame hides itself when neither has anything to say. -->
						<div
							class="mb-4 flex flex-col gap-2 [&:not(:has(*))]:hidden [&>*]:rounded-lg [&>*]:border"
						>
							<RepoBanner project={scope.project} />
							<AdoptBanner
								project={scope.project}
								namespace={scope.kind === 'namespace' ? scope.namespace : undefined}
							/>
						</div>
					{/if}
					<ClusterSummary
						scope={containerScope}
						vms={scopedVMs}
						onselect={openVM}
						hosts={section === 'hosts' ? facts : undefined}
					/>
				{/if}
			{:else if tab === 'templates'}
				<TemplatesTab project={scope.kind === 'project' ? scope.project : undefined} />
			{:else if tab === 'monitor'}
				<ContainerMonitor namespaces={scopedNamespaces} scope={containerScope} onselect={openVM} />
			{:else if tab === 'permissions'}
				<Permissions namespaces={scopedNamespaces} />
			{:else if tab === 'configure'}
				{#if scope.kind === 'node'}
					<NodeConfigure node={scope.node} vms={scopedVMs} />
				{:else}
					<ContainerConfigure projects={cfgProjects} cluster={root && section === 'hosts'} />
				{/if}
			{:else if tab === 'security' && scope.kind === 'namespace'}
				<EffectivePolicyPanel namespace={scope.namespace} />
			{/if}
		</div>
	{/if}
</div>

{#if bulkCtx}
	<BulkActionsMenu
		x={bulkCtx.x}
		y={bulkCtx.y}
		count={picked.size}
		{powerWhy}
		{deleteWhy}
		onclose={() => (bulkCtx = null)}
		onpower={bulkPower}
		ondelete={() => (confirmingBulkDelete = true)}
		onclear={() => (picked = new Set())}
	/>
{/if}

{#if confirmingBulkDelete}
	<BulkDeleteConfirm
		vms={pickedVMs}
		busy={bulkOp.busy}
		onconfirm={bulkDelete}
		onclose={() => (confirmingBulkDelete = false)}
	/>
{/if}
