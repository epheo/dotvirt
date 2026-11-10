<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { FolderPlus, TriangleAlert } from '@lucide/svelte';
	import '../app.css';
	import favicon from '#lib/assets/favicon.svg';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import {
		api,
		onUnauthorized,
		type Alert,
		type Modules,
		streamInventory,
		Unauthorized,
		type StreamStatus,
	} from '#lib/api.js';
	import { friendlyError } from '#lib/format.js';
	import { resource } from '#lib/resource.svelte.js';
	import { INVENTORY_SECTIONS, sectionOf, vmHref, type Section } from '#lib/nav.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { templates } from '#lib/state/templates.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { gateways } from '#lib/state/gateways.svelte.js';
	import { replication } from '#lib/state/replication.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { clusterGate } from '#lib/gates.js';
	import { caps } from '#lib/state/caps.svelte.js';
	import { modules } from '#lib/state/modules.svelte.js';
	import { lastSection } from '#lib/state/nav.svelte.js';
	import { session } from '#lib/state/session.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import AppContextMenus from '#lib/components/AppContextMenus.svelte';
	import Button from '#lib/components/Button.svelte';
	import ChangeTray from '#lib/components/ChangeTray.svelte';
	import AppHeader from '#lib/components/AppHeader.svelte';
	import AppModals from '#lib/components/AppModals.svelte';
	import Banner from '#lib/components/Banner.svelte';
	import Login from '#lib/components/Login.svelte';
	import SSOBanner from '#lib/components/SSOBanner.svelte';
	import SectionSwitcher from '#lib/components/SectionSwitcher.svelte';
	import Skeleton from '#lib/components/Skeleton.svelte';
	import TaskDock from '#lib/components/TaskDock.svelte';
	import ToastHost from '#lib/components/ToastHost.svelte';
	import ChangesTree from '#lib/tree/ChangesTree.svelte';
	import ComputeTree from '#lib/tree/ComputeTree.svelte';
	import HostsTree from '#lib/tree/HostsTree.svelte';
	import NetworkingTree from '#lib/tree/NetworkingTree.svelte';
	import StorageTree from '#lib/tree/StorageTree.svelte';

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
	// Signed out, not merely still checking: the tray's followed PRs persist
	// across a reload and must survive the boot probe.
	$effect(() => {
		if (!session.user && !session.checking) {
			session.logoutError = '';
			inventory.reset();
			drafts.reset();
			reviewCache.reset();
			ui.reset();
			templates.reset();
			storage.reset();
			replication.reset();
			gateways.reset();
			caps.reset();
			modules.value = null;
			tray.reset();
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

	// The network catalog and the policy plane: re-pulled on sign-in AND
	// whenever networksVersion bumps (GitOps/git moved, or a policy reflector
	// fired NetworkChanged), so a merged network or firewall rule appears
	// without a reload. Keyed on the stable derived primitive, so it fires only
	// when networking may have changed, not every VM-state frame. The pull
	// retries with backoff (its only fetch: a transient boot error would
	// otherwise blank the networking views for the session) and stops when this
	// effect re-runs or tears down. One that outlasts its retries is named in a
	// banner whose Retry is one attempt.
	$effect(() => {
		if (!session.user) return;
		inventory.networksVersion; // subscribe: re-pull when GitOps/git moves
		const owner = new AbortController();
		void untrack(() => inventory.loadNet({ signal: owner.signal }));
		return () => owner.abort();
	});

	// What the caller may do can move with a merge (a grant, a new platform
	// object) or a network or policy change: the same fingerprints drop every
	// cached answer, and each control asks again.
	$effect(() => {
		if (!session.user) return;
		void `${inventory.networksVersion}|${inventory.tasksVersion}|${inventory.replicationVersion !== 0}|${inventory.routesVersion !== 0}`;
		untrack(() => caps.invalidate());
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

	// Firing cluster alerts join the issues plane: one cached instant query
	// server-side, polled slowly while signed in. A failed read marks the feed
	// unavailable rather than reading as "none firing".
	const alarms = resource<Alert[]>(
		() => (session.user ? 'on' : ''),
		(k) => (k ? api.alarms() : Promise.resolve([])),
		{ poll: () => (session.user ? 30000 : 0) },
	);
	$effect(() => {
		inventory.alerts = alarms.failed ? null : (alarms.data ?? []);
	});

	// Kept across a failed read: a blip must not bring dead tiles back.
	const moduleRead = resource<Modules | null>(
		() => (session.user ? 'on' : ''),
		(k) => (k ? api.modules() : Promise.resolve(null)),
		{ poll: () => (session.user ? 60000 : 0) },
	);
	$effect(() => {
		if (moduleRead.data) modules.value = moduleRead.data;
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
		void clusterGate.platform().ok; // platform stagers also carry a platform draft
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

		{#if session.logoutError}
			<Banner tone="danger" size="md">
				<TriangleAlert size={16} class="shrink-0" />
				<span>{session.logoutError}</span>
				<Button
					variant="link"
					size="sm"
					disabled={session.signingOut}
					aria-label="Retry sign-out"
					onclick={() => session.logout()}>{session.signingOut ? 'Signing out...' : 'Retry'}</Button
				>
			</Banner>
		{/if}

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
				<Button
					variant="link"
					size="sm"
					disabled={inventory.netBusy}
					onclick={() => inventory.loadNet({ retries: 0 })}
					>{inventory.netBusy ? 'Retrying...' : 'Retry'}</Button
				>
			</Banner>
		{/if}

		<SSOBanner />

		{#if inventory.inventory?.warnings?.length}
			<Banner tone="warn" size="md" class="items-start">
				<TriangleAlert size={16} class="mt-0.5 shrink-0" />
				<span>{inventory.inventory.warnings.join('; ')}</span>
			</Banner>
		{/if}

		<div class="flex min-h-0 flex-1" data-workspace>
			<aside class="flex w-64 flex-col border-r border-side-line bg-side">
				<SectionSwitcher active={treeSection} />
				<div class="min-h-0 flex-1 overflow-y-auto">
					{#if !inventory.inventory}
						<div class="p-3"><Skeleton rows={5} class="h-5" bg="bg-side-hover" /></div>
					{:else if inventory.projects.length === 0 && inventory.adoptable.length === 0 && treeSection !== 'changes'}
						<div class="space-y-3 p-6 text-center">
							<p class="text-xs text-side-dim">No projects visible.</p>
							{#if clusterGate.namespaceCreate().ok}
								<Button size="sm" onclick={() => (ui.modal = { kind: 'newProject' })}>
									<FolderPlus size={14} /> Create your first project
								</Button>
							{/if}
						</div>
					{:else if treeSection === 'compute'}
						<ComputeTree />
					{:else if treeSection === 'hosts'}
						{#if clusterGate.hosts().ok}<HostsTree />{/if}
					{:else if treeSection === 'networking'}
						<NetworkingTree />
					{:else if treeSection === 'storage'}
						<StorageTree />
					{:else}
						<ChangesTree />
					{/if}
				</div>
			</aside>
			<main class="relative flex min-w-0 flex-1 flex-col overflow-hidden bg-panel">
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
		>
			{#snippet trailing()}<ChangeTray />{/snippet}
		</TaskDock>

		<AppModals />
		<AppContextMenus />
		<ToastHost />
	</div>
{/if}
