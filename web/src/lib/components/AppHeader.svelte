<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import {
		BookCopy,
		Check,
		ChevronDown,
		CircleCheck,
		ClipboardList,
		FolderPlus,
		Monitor,
		Moon,
		Network,
		Plus,
		Radio,
		Server,
		Shield,
		Sun,
		TriangleAlert,
		Upload,
		User as UserIcon,
	} from 'lucide-svelte';
	import { dispatchVMAction } from '$lib/actions';
	import { hrefForScope, scopeFromPath, vmHref } from '$lib/nav';
	import StatusDot from './StatusDot.svelte';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { session } from '$lib/state/session.svelte';
	import { theme } from '$lib/state/theme.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import GlobalSearch, { type SearchHit } from './GlobalSearch.svelte';
	import HeaderMenu from './HeaderMenu.svelte';
	import MenuItem from './MenuItem.svelte';

	// Repo-backed namespaces under the current URL's scope - what "New VM"
	// pre-targets, mirroring the tree context menu's "New VM here". null = no
	// scope narrowing, so the wizard offers every creatable namespace.
	const scopeNamespaces = $derived.by(() => {
		const sc = scopeFromPath(page.url.pathname);
		if (sc.kind === 'project' || sc.kind === 'namespace') {
			const p = inventory.inventory?.projects.find((proj) => proj.name === sc.project);
			if (!p?.repo) return null;
			return sc.kind === 'namespace' ? [sc.namespace] : p.namespaces.map((n) => n.namespace);
		}
		return null;
	});

	// Global search: a hit either opens a VM or focuses its scope.
	function onSearchPick(hit: SearchHit) {
		switch (hit.kind) {
			case 'action':
				dispatchVMAction(hit.action, hit.vm, { onstaged: () => drafts.refresh() });
				break;
			case 'vm':
				goto(vmHref(hit.vm.namespace, hit.vm.name));
				break;
			case 'project':
				goto(hrefForScope({ kind: 'project', project: hit.project }));
				break;
			case 'namespace':
				goto(hrefForScope({ kind: 'namespace', project: hit.project, namespace: hit.namespace }));
				break;
			case 'node':
				goto(hrefForScope({ kind: 'node', node: hit.node }));
				break;
			case 'network':
				goto(hrefForScope({ kind: 'network', network: hit.network }));
				break;
			case 'storage':
				goto(hrefForScope({ kind: 'storage', storageClass: hit.storageClass }));
				break;
		}
	}

	// The Review-changes badge: staged edits + open PRs, everything in flight.
	const inFlight = $derived(drafts.count + inventory.proposals.length);

	// The issues bell: standing problems derived from the live stream, so the
	// count moves with the same frames the tree and grid repaint on.
	const issues = $derived(inventory.issues);
	const worstTone = $derived(issues.some((i) => i.severity === 'danger') ? 'danger' : 'warn');

	// Object pages push label queries into the masthead search via ui.search.
	let search = $state<GlobalSearch | null>(null);
	$effect(() => {
		ui.search = search;
		return () => (ui.search = null);
	});

	// The "New" menu, one row per creatable kind. divider starts the
	// platform-tier section. newVM's payload alone carries extra state
	// (scopeNamespaces), added at dispatch.
	type NewItem = {
		icon: typeof Server;
		label: string;
		kind: 'newVM' | 'deployTemplate' | 'newNetwork' | 'upload' | 'newProject' | 'tier0' | 'adminFw';
		enabled: boolean;
		title: string;
		divider?: boolean;
	};
	const NEW_ITEMS = $derived.by((): NewItem[] => {
		const noRepo = !inventory.namespaces.length;
		const repoTitle = noRepo ? 'No project with a backing repo yet' : '';
		return [
			{
				icon: Server,
				label: 'New VM',
				kind: 'newVM',
				enabled: !noRepo,
				title: repoTitle,
			},
			{
				icon: BookCopy,
				label: 'New VM from Template',
				kind: 'deployTemplate',
				enabled: !noRepo,
				title: repoTitle,
			},
			{
				icon: Network,
				label: 'New Segment',
				kind: 'newNetwork',
				enabled: !noRepo,
				title: 'Create a Segment (Port Group) — an overlay or VLAN Layer 2 network VMs attach to',
			},
			{
				icon: Upload,
				label: 'Upload Image',
				kind: 'upload',
				enabled: !noRepo,
				title: 'Upload a disk image (qcow2/raw/iso) as a bootable DataVolume',
			},
			{
				icon: FolderPlus,
				label: 'New Project',
				kind: 'newProject',
				enabled: inventory.canNamespace,
				title: inventory.canNamespace
					? 'Create a new tenant project (repo + first namespace)'
					: 'Requires permission to create namespaces',
				divider: true,
			},
			{
				icon: Radio,
				label: 'New Tier-0 Service',
				kind: 'tier0',
				enabled: inventory.canEgress,
				title: inventory.canEgress
					? 'Add a Tier-0 provider-edge service (Source NAT or external route)'
					: 'Requires permission to create EgressIPs or external routes',
			},
			{
				icon: Shield,
				label: 'New Admin Firewall',
				kind: 'adminFw',
				enabled: inventory.canAdminFw,
				title: inventory.canAdminFw
					? 'Add a cluster-wide admin firewall (AdminNetworkPolicy / Baseline)'
					: 'Requires permission to create AdminNetworkPolicies',
			},
		];
	});
</script>

<!-- The bar stays dark in both themes, so its chrome uses raw slate, not ink tokens. -->
<header class="flex items-center gap-3 border-b border-line-strong bg-bar px-4 py-2 text-white">
	<a href="/compute" class="font-semibold">dotvirt</a>

	<GlobalSearch bind:this={search} onpick={onSearchPick} />

	<!-- Create actions collapse into one primary menu (the global chrome stays
	     identity + search + tasks; creation is otherwise contextual via
	     the tree's right-click menus). New VM pre-targets the current scope. -->
	<HeaderMenu>
		{#snippet trigger({ open, toggle })}
			<button
				onclick={toggle}
				class="flex items-center gap-1.5 rounded-full bg-accent px-3 py-1 text-xs font-medium text-white hover:bg-accent-hover"
			>
				<Plus size={14} /> New <ChevronDown
					size={12}
					class="transition-transform {open ? 'rotate-180' : ''}"
				/>
			</button>
		{/snippet}
		{#snippet children({ close })}
			{#each NEW_ITEMS as item (item.kind)}
				{@const Icon = item.icon}
				{#if item.divider}
					<div class="my-1 border-t border-line-soft"></div>
				{/if}
				<MenuItem
					onclick={() => {
						close();
						ui.modal =
							item.kind === 'newVM'
								? { kind: 'newVM', namespaces: scopeNamespaces }
								: { kind: item.kind };
					}}
					disabled={!item.enabled}
					title={item.title}
				>
					{#snippet icon()}<Icon size={13} />{/snippet}
					{item.label}
				</MenuItem>
			{/each}
		{/snippet}
	</HeaderMenu>

	<!-- Issues: the attention bell - standing problems only (never pending
	     applies), so a lit badge always means something is actually wrong. -->
	<HeaderMenu>
		{#snippet trigger({ open, toggle })}
			<button
				onclick={toggle}
				title="Issues — standing problems in the visible inventory"
				class="relative rounded p-1.5 hover:bg-side-hover {open
					? 'bg-side-hover text-white'
					: 'text-side-dim'}"
			>
				<TriangleAlert size={16} />
				{#if issues.length > 0}
					<span
						class="absolute -top-1 -right-1 rounded-full px-1 text-[10px] font-medium text-white {worstTone ===
						'danger'
							? 'bg-danger'
							: 'bg-warn'}">{issues.length}</span
					>
				{/if}
			</button>
		{/snippet}
		{#snippet children({ close })}
			<div class="w-96 max-w-[90vw]">
				{#if !issues.length}
					<p class="flex items-center gap-2 px-3 py-2.5 text-xs text-ink-faint">
						<CircleCheck size={14} class="text-ok" /> No standing issues.
					</p>
				{:else}
					<ul class="max-h-96 divide-y divide-line-soft overflow-y-auto text-xs">
						{#each issues.slice(0, 12) as i (i.scope + i.label)}
							<li>
								<button
									onclick={() => {
										close();
										goto(i.href);
									}}
									class="flex w-full items-baseline gap-2 px-3 py-1.5 text-left hover:bg-inset"
									title={i.detail ?? ''}
								>
									<span class="self-center"><StatusDot tone={i.severity} size="xs" /></span>
									<span class="shrink-0 font-medium text-ink">{i.scope}</span>
									<span class="truncate text-ink-soft">{i.label}</span>
								</button>
							</li>
						{/each}
					</ul>
					{#if issues.length > 12}
						<p class="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-ink-faint">
							and {issues.length - 12} more
						</p>
					{/if}
				{/if}
			</div>
		{/snippet}
	</HeaderMenu>

	<!-- Review changes: the GitOps write model's front door - a labeled peer of
	     New, since reviewing and proposing is the product's primary loop. The
	     badge counts staged edits + open PRs. -->
	<a
		href="/changes"
		title="Review staged changes and open pull requests"
		class="relative flex items-center gap-1.5 rounded border border-side-active px-2.5 py-1 text-xs font-medium hover:bg-side-hover {page
			.url.pathname === '/changes'
			? 'bg-side-hover text-white'
			: 'text-side-ink'}"
	>
		<ClipboardList size={13} /> Review changes
		{#if inFlight > 0}
			<span
				class="absolute -top-1.5 -right-1.5 rounded-full bg-accent-hover px-1 text-[10px] font-medium text-white"
				>{inFlight}</span
			>
		{/if}
	</a>

	<HeaderMenu align="right" class="ml-auto">
		{#snippet trigger({ open, toggle })}
			<button
				onclick={toggle}
				class="flex items-center gap-1.5 rounded px-2 py-1 text-xs text-side-ink hover:bg-side-hover"
			>
				<UserIcon size={14} />
				{session.user?.username}
				<ChevronDown size={12} class="transition-transform {open ? 'rotate-180' : ''}" />
			</button>
		{/snippet}
		{#snippet children({ close })}
			<div class="border-b border-line-soft px-3 py-2">
				<div class="font-medium text-ink">{session.user?.username}</div>
				{#if session.user?.groups.length}
					<div class="mt-0.5 text-[11px] break-words text-ink-faint">
						{session.user.groups.join(', ')}
					</div>
				{/if}
			</div>
			<div class="px-3 py-1.5 text-ink-muted">{inventory.vmCount} VMs in view</div>
			<div class="border-t border-line-soft"></div>
			<div class="px-3 pt-1.5 pb-0.5 text-[10px] tracking-wide text-ink-faint uppercase">Theme</div>
			{#each [{ id: 'light', label: 'Light' }, { id: 'dark', label: 'Dark' }, { id: 'system', label: 'System' }] as const as opt (opt.id)}
				<MenuItem onclick={() => (theme.mode = opt.id)}>
					{#snippet icon()}
						{#if opt.id === 'light'}<Sun size={13} />{:else if opt.id === 'dark'}<Moon
								size={13}
							/>{:else}<Monitor size={13} />{/if}
					{/snippet}
					{opt.label}
					{#if theme.mode === opt.id}<Check size={13} class="ml-auto text-accent-ink" />{/if}
				</MenuItem>
			{/each}
			<div class="border-t border-line-soft"></div>
			<MenuItem
				onclick={() => {
					close();
					session.logout();
				}}
			>
				Sign out
			</MenuItem>
		{/snippet}
	</HeaderMenu>
</header>
