<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { FolderPlus, TriangleAlert } from 'lucide-svelte';
	import '../app.css';
	import favicon from '#lib/assets/favicon.svg';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import {
		api,
		onUnauthorized,
		streamInventory,
		Unauthorized,
		withRetry,
		type StreamStatus,
	} from '#lib/api.js';
	import { friendlyError } from '#lib/format.js';
	import { INVENTORY_SECTIONS, sectionOf, vmHref, type Section } from '#lib/nav.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { lastSection } from '#lib/state/nav.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import AppContextMenus from '#lib/components/AppContextMenus.svelte';
	import Button from '#lib/components/Button.svelte';
	import AppHeader from '#lib/components/AppHeader.svelte';
	import AppModals from '#lib/components/AppModals.svelte';
	import Banner from '#lib/components/Banner.svelte';
	import Login from '#lib/components/Login.svelte';
	import SSOBanner from '#lib/components/SSOBanner.svelte';
	import SectionSwitcher from '#lib/components/SectionSwitcher.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import TaskDock from '#lib/components/TaskDock.svelte';
	import ToastHost from '#lib/components/ToastHost.svelte';
	import CatalogTree from '#lib/tree/CatalogTree.svelte';
	import ChangesTree from '#lib/tree/ChangesTree.svelte';
	import ComputeTree from '#lib/tree/ComputeTree.svelte';
	import FlatSectionTree from '#lib/tree/FlatSectionTree.svelte';

	let { children }: { children: Snippet } = $props();

	// The tree follows the section the URL is in, but sticks across the
	// section-agnostic /vm route - a VM opened from the Hosts tree keeps the
	// Hosts tree (and its highlighted row). An inventory section also persists
	// as where "/" lands next visit; Changes is a section but never a home.
	let treeSection = $state<Section>('compute');
	$effect(() => {
		const s = sectionOf(page.url.pathname);
		if (s) {
			treeSection = s;
			if (INVENTORY_SECTIONS.includes(s)) lastSection.value = s;
		}
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
			reviewCache.reset();
			ui.reset();
			catalog.reset();
		}
	});

	$effect(() => {
		session.check();
	});

	// Live subscription, established once signed in. The cookie rides the handshake;
	// a 401 on the upgrade (expired session) drops us back to login. The banner
	// names a lost stream (the tree is a stale snapshot meanwhile) and offers a
	// retry that skips the backoff wait.
	let liveStream: { retryNow: () => void } | null = null;
	const STREAM_LOST: Record<StreamStatus, string> = {
		connecting: '',
		live: '',
		reconnecting: 'Live updates lost. Reconnecting…',
		down: 'Live updates down: the server is not answering. Still retrying.',
	};
	$effect(() => {
		if (!session.user) return;
		inventory.inventory = null;
		const s = streamInventory(
			(inv) => inventory.apply(inv),
			() => (session.user = null),
			(status) => {
				inventory.stream = status;
				inventory.error = STREAM_LOST[status];
			},
		);
		liveStream = s;
		return () => {
			s.close();
			liveStream = null;
		};
	});

	// The port-group catalog and the policy plane are fetched out-of-band (not on the
	// live frame). Re-pull both on sign-in AND whenever networksVersion bumps
	// (GitOps/git moved, or a policy reflector fired NetworkChanged), so a merged
	// segment or firewall rule appears without a reload. Keyed on the stable derived
	// primitive, so it fires only when networking may have changed - not every
	// VM-state frame. A failed pull retries with backoff: this is the only fetch of
	// these planes, so a transient boot error would otherwise blank the networking
	// and host-configure views for the whole session. 401 signs out centrally.
	// Only the pull for the current version writes: version bumps come in bursts,
	// and an older response landing last would paint stale planes (or, after
	// sign-out, repopulate the reset stores).
	// A pull that outlasts its retries is named in a banner whose Retry bumps
	// netRetry; the last good planes stay rendered meanwhile.
	let netRetry = $state(0);
	$effect(() => {
		if (!session.user) return;
		inventory.networksVersion; // subscribe: re-pull when GitOps/git moves
		netRetry;
		let live = true;
		withRetry(() => Promise.all([api.networks(), api.policies()]))
			.then(([n, p]) => {
				if (!live) return;
				inventory.netInv = n;
				inventory.polInv = p;
				inventory.netError = '';
			})
			.catch((e) => {
				if (live && !(e instanceof Unauthorized)) inventory.netError = friendlyError(e);
			});
		return () => {
			live = false;
		};
	});

	// The options catalog, once per session: the storage lens groups classless
	// disks under the real default class, read views resolve instancetype sizing.
	$effect(() => {
		if (!session.user) return;
		inventory.loadOptions();
	});

	// The recent-tasks feed rides the same out-of-band contract: re-pull when an
	// op is recorded or a merged PR lands (tasksVersion), not on every VM frame.
	let tasksRetry = $state(0);
	$effect(() => {
		if (!session.user) return;
		inventory.tasksVersion; // subscribe
		tasksRetry;
		let live = true;
		api
			.tasks()
			.then((t) => {
				if (!live) return;
				inventory.taskFeed = t;
				inventory.tasksError = '';
			})
			.catch((e) => {
				if (live && !(e instanceof Unauthorized)) inventory.tasksError = friendlyError(e);
			});
		return () => {
			live = false;
		};
	});

	// Recompute the draft summary only when the SET of projects or PR lanes
	// changes, or once the first frame lands (an empty project set is an answer
	// too, or the Changes lanes would wait forever): depend on stable keys, never
	// the per-frame arrays, which would re-fire on every VM state change.
	// Staging actions call drafts.refresh directly.
	const streamed = $derived(inventory.inventory !== null);
	$effect(() => {
		inventory.projectKey;
		inventory.proposalsKey;
		inventory.canManage; // platform authors also carry a platform draft
		if (session.user && streamed) untrack(() => drafts.refresh());
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<title>dotvirt</title>
</svelte:head>

{#if session.checking}
	<div class="flex h-screen flex-col items-center justify-center gap-3 text-sm text-ink-faint">
		{#if session.bootError}
			<p class="font-medium text-ink-soft">
				{session.bootError.answered ? 'Server error' : 'Server unreachable'}. Retrying…
			</p>
			<p class="max-w-md text-center font-mono text-xs break-all">{session.bootError.detail}</p>
			<Button variant="secondary" size="sm" onclick={() => session.retryNow()}>Retry now</Button>
		{:else}
			Loading…
		{/if}
	</div>
{:else if !session.user}
	<Login onlogin={(u) => (session.user = u)} />
{:else}
	<div class="flex h-screen flex-col">
		<AppHeader />

		{#if inventory.error}
			<Banner tone={inventory.stream === 'down' ? 'danger' : 'warn'} size="md">
				<TriangleAlert size={16} class="shrink-0" />
				<span>{inventory.error}</span>
				<Button variant="link" size="sm" onclick={() => liveStream?.retryNow()}>Retry</Button>
			</Banner>
		{/if}

		{#if inventory.netError}
			<Banner tone="warn" size="md">
				<TriangleAlert size={16} class="shrink-0" />
				<span class="min-w-0"
					>Networking and security views may be out of date: {inventory.netError}</span
				>
				<Button variant="link" size="sm" onclick={() => netRetry++}>Retry</Button>
			</Banner>
		{/if}

		<SSOBanner />

		{#if inventory.inventory?.warnings?.length}
			<Banner tone="warn" size="md" class="items-start">
				<TriangleAlert size={16} class="mt-0.5 shrink-0" />
				<span>{inventory.inventory.warnings.join('; ')}</span>
			</Banner>
		{/if}

		<div class="flex min-h-0 flex-1">
			<aside class="flex w-72 flex-col border-r border-side-line bg-side">
				<SectionSwitcher active={treeSection} />
				<div class="min-h-0 flex-1 overflow-y-auto">
					{#if !inventory.inventory}
						<div class="p-3"><Skeleton rows={5} class="h-5" bg="bg-side-hover" /></div>
					{:else if inventory.projects.length === 0 && inventory.adoptable.length === 0 && treeSection !== 'catalog' && treeSection !== 'changes'}
						<div class="space-y-3 p-6 text-center">
							<p class="text-xs text-side-dim">No projects visible.</p>
							{#if inventory.canNamespace}
								<Button size="sm" onclick={() => (ui.modal = { kind: 'newProject' })}>
									<FolderPlus size={14} /> Create your first project
								</Button>
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
					{:else if treeSection === 'changes'}
						<ChangesTree />
					{:else}
						<CatalogTree />
					{/if}
				</div>
			</aside>
			<main class="flex min-w-0 flex-1 flex-col overflow-hidden bg-panel">
				{@render children()}
			</main>
		</div>

		<TaskDock
			drafts={drafts.drafts}
			proposals={inventory.proposals}
			tasks={inventory.taskFeed}
			tasksError={inventory.tasksError}
			inventory={inventory.inventory}
			username={session.user.username}
			onselect={(namespace, name) => goto(vmHref(namespace, name))}
			onrefresh={() => {
				drafts.refresh();
				tasksRetry++;
			}}
		/>

		<AppModals />
		<AppContextMenus />
		<ToastHost />
	</div>
{/if}
