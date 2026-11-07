<script lang="ts">
	import { page } from '$app/state';
	import {
		BookCopy,
		ChevronDown,
		FolderPlus,
		Network,
		Plus,
		Radio,
		Server,
		Shield,
		Upload,
	} from '@lucide/svelte';
	import { scopeFromPath } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import HeaderMenu from './HeaderMenu.svelte';
	import MenuItem from './MenuItem.svelte';

	// Repo-backed namespaces under the current URL's scope - what "New VM"
	// pre-targets, mirroring the tree context menu's "New VM here". null = no
	// scope narrowing, so the wizard offers every creatable namespace.
	const scopeNamespaces = $derived.by(() => {
		const sc = scopeFromPath(page.url.pathname);
		if (sc.kind === 'project' || sc.kind === 'namespace') {
			const p = inventory.projects.find((proj) => proj.name === sc.project);
			if (!p?.repo) return null;
			return sc.kind === 'namespace' ? [sc.namespace] : p.namespaces.map((n) => n.namespace);
		}
		return null;
	});
	// The namespace every create form preselects: the one in view (a namespace
	// scope, a VM page, or a project's first namespace). Empty leaves the form
	// to its own first choice.
	const scopeNamespace = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		if (parts[0] === 'vm' && parts.length >= 2) return parts[1];
		return scopeNamespaces?.[0] ?? '';
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
		const repoTitle = !noRepo
			? ''
			: inventory.repoNamespaces.length
				? 'Read-only: you may not edit VMs in any namespace'
				: 'No project with a backing repo yet';
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
				enabled: !noRepo || inventory.canManage,
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

<!-- Create actions collapse into one primary menu (the global chrome stays
     identity + search + tasks; creation is otherwise contextual via
     the tree's right-click menus). New VM pre-targets the current scope. -->
<HeaderMenu>
	{#snippet trigger({ open, toggle })}
		<Button onclick={toggle}>
			<Plus size={14} /> New <ChevronDown
				size={12}
				class="transition-transform {open ? 'rotate-180' : ''}"
			/>
		</Button>
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
					const namespace = scopeNamespace || undefined;
					ui.modal =
						item.kind === 'newVM'
							? { kind: 'newVM', namespaces: scopeNamespaces, namespace }
							: item.kind === 'newNetwork' ||
								  item.kind === 'upload' ||
								  item.kind === 'deployTemplate'
								? { kind: item.kind, namespace }
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
