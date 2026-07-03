<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { FolderPlus } from 'lucide-svelte';
	import '../app.css';
	import favicon from '$lib/assets/favicon.svg';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, onUnauthorized, streamInventory } from '$lib/api';
	import { sectionOf, vmHref, type Section } from '$lib/nav';
	import { drafts, PLATFORM_PROJECT } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { session } from '$lib/state/session.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import AppContextMenus from '$lib/components/AppContextMenus.svelte';
	import AppHeader from '$lib/components/AppHeader.svelte';
	import AppModals from '$lib/components/AppModals.svelte';
	import ChangesPanel from '$lib/components/ChangesPanel.svelte';
	import Login from '$lib/components/Login.svelte';
	import SectionSwitcher from '$lib/components/SectionSwitcher.svelte';
	import TaskDock from '$lib/components/TaskDock.svelte';
	import ToastHost from '$lib/components/ToastHost.svelte';
	import CatalogTree from '$lib/tree/CatalogTree.svelte';
	import ComputeTree from '$lib/tree/ComputeTree.svelte';
	import FlatSectionTree from '$lib/tree/FlatSectionTree.svelte';

	let { children }: { children: Snippet } = $props();

	// The tree follows the section the URL is in, but sticks across the
	// section-agnostic /vm route — a VM opened from the Hosts tree keeps the
	// Hosts tree (and its highlighted row), as vCenter does.
	let treeSection = $state<Section>('compute');
	$effect(() => {
		if (page.url.pathname.split('/')[1] !== 'vm') treeSection = sectionOf(page.url.pathname);
	});

	// Drop to the login screen on any 401. Registered as the api layer's one
	// signed-out sink, so every fetching component is covered without threading
	// a callback; local Unauthorized catches only suppress their own error UI.
	onUnauthorized(() => {
		session.user = null;
	});
	$effect(() => {
		if (!session.user) {
			inventory.reset();
			drafts.reset();
			ui.reset();
		}
	});

	$effect(() => {
		session.check();
	});

	// Live subscription, established once signed in. The cookie rides the handshake;
	// a 401 on the upgrade (expired session) drops us back to login.
	$effect(() => {
		if (!session.user) return;
		inventory.inventory = null;
		const stop = streamInventory(
			(inv) => inventory.apply(inv),
			() => (session.user = null)
		);
		return stop;
	});

	// The port-group catalog: fetched once on sign-in. A failure (e.g. the OVN-K
	// CRDs absent) leaves it empty — NICs then fall back to their raw refs.
	$effect(() => {
		if (!session.user) return;
		api
			.networks()
			.then((n) => (inventory.netInv = n))
			.catch(() => {}); // a failure just leaves the catalog empty; 401 signs out centrally
	});

	// Recompute the draft summary only when the SET of projects or PR lanes
	// changes: depend on the stable keys, and read the project list via untrack so
	// the effect doesn't also subscribe to the per-frame array reference (which
	// would re-fire on every VM state change). Staging actions call drafts.refresh
	// directly.
	$effect(() => {
		inventory.projectKey;
		inventory.proposalsKey;
		untrack(() => {
			if (session.user && inventory.projectNames.length) drafts.refresh();
		});
	});

	// Opening the Changes drawer re-reads the draft summary, so what it shows is
	// current at open — the keyed effect above only makes it eventually current.
	$effect(() => {
		if (ui.changesOpen) untrack(() => drafts.refresh());
	});

	const canNamespace = $derived(!!inventory.caps?.namespace);
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<title>dotvirt</title>
</svelte:head>

{#if session.checking}
	<div class="flex h-screen items-center justify-center text-sm text-ink-faint">Loading…</div>
{:else if !session.user}
	<Login onlogin={(u) => (session.user = u)} />
{:else}
	<div class="flex h-screen flex-col">
		<AppHeader />

		{#if inventory.error}
			<div
				class="flex items-start gap-2 border-b border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700"
			>
				<span class="font-medium">Error:</span>
				<span class="font-mono text-xs break-all">{inventory.error}</span>
			</div>
		{/if}

		{#if inventory.inventory?.warnings?.length}
			<div
				class="flex items-start gap-2 border-b border-amber-200 bg-amber-50 px-4 py-2 text-sm text-amber-800"
			>
				<span class="font-medium">⚠</span>
				<span>{inventory.inventory.warnings.join('; ')}</span>
			</div>
		{/if}

		<div class="flex min-h-0 flex-1">
			<aside class="flex w-72 flex-col border-r border-line-strong bg-panel">
				<SectionSwitcher active={treeSection} />
				<div class="min-h-0 flex-1 overflow-y-auto">
					{#if !inventory.inventory}
						<div class="space-y-2 p-3">
							{#each Array(5) as _, i (i)}
								<div class="h-5 animate-pulse rounded bg-slate-100"></div>
							{/each}
						</div>
					{:else if inventory.inventory.projects.length === 0 && treeSection !== 'catalog'}
						<div class="space-y-3 p-6 text-center">
							<p class="text-xs text-ink-faint">No projects visible.</p>
							{#if canNamespace}
								<button
									onclick={() => (ui.modal = { kind: 'newProject' })}
									class="inline-flex items-center gap-1.5 rounded bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
								>
									<FolderPlus size={14} /> Create your first project
								</button>
							{/if}
						</div>
					{:else if treeSection === 'compute'}
						<ComputeTree />
					{:else if treeSection === 'hosts'}
						<FlatSectionTree kind="node" />
					{:else if treeSection === 'networking'}
						<FlatSectionTree kind="network" />
					{:else if treeSection === 'storage'}
						<FlatSectionTree kind="storage" />
					{:else}
						<CatalogTree />
					{/if}
				</div>
			</aside>
			<main class="flex min-w-0 flex-1 flex-col overflow-hidden bg-panel">
				{@render children()}
			</main>

			{#if ui.changesOpen}
				<ChangesPanel
					drafts={drafts.drafts}
					proposals={inventory.proposals}
					projects={inventory.canManage
						? [...inventory.repoProjects, PLATFORM_PROJECT]
						: inventory.repoProjects}
					loaded={drafts.loaded}
					refreshing={drafts.refreshing}
					onclose={() => (ui.changesOpen = false)}
					onchanged={() => drafts.refresh()}
				/>
			{/if}
		</div>

		<TaskDock
			drafts={drafts.drafts}
			proposals={inventory.proposals}
			actions={ui.recentActions}
			inventory={inventory.inventory}
			username={session.user.username}
			onselect={(namespace, name) => goto(vmHref(namespace, name))}
			onrefresh={() => drafts.refresh()}
		/>

		<AppModals />
		<AppContextMenus />
		<ToastHost />
	</div>
{/if}
