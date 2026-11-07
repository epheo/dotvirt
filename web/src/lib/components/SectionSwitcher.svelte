<script lang="ts">
	import { ClipboardList, Database, Folder, Library, Network, Server } from '@lucide/svelte';
	import type { Section } from '#lib/nav.js';

	// The section switcher: six sections, each with its own tree below, all
	// rendering into the same center workspace. Work in flight lives in the
	// change tray, not on the switcher.
	let { active }: { active: Section } = $props();

	const SECTIONS: { id: Section; label: string; href: string }[] = [
		{ id: 'compute', label: 'Compute', href: '/compute' },
		{ id: 'hosts', label: 'Hosts', href: '/hosts' },
		{ id: 'networking', label: 'Networking', href: '/networking' },
		{ id: 'storage', label: 'Storage', href: '/storage' },
		{ id: 'catalog', label: 'Catalog', href: '/catalog' },
		{ id: 'changes', label: 'Changes', href: '/changes' },
	];
</script>

<!-- Two compact rows of three, each icon beside its name. -->
<nav class="grid grid-cols-3 border-b border-side-line">
	{#each SECTIONS as s (s.id)}
		<a
			href={s.href}
			title={s.label}
			class="relative flex min-w-0 items-center gap-1 px-1.5 py-2 text-xs {active === s.id
				? 'bg-side-active font-medium text-side-ink shadow-[inset_0_-2px_0_var(--color-accent-hover)]'
				: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
		>
			{#if s.id === 'compute'}<Folder size={14} class="shrink-0" />
			{:else if s.id === 'hosts'}<Server size={14} class="shrink-0" />
			{:else if s.id === 'networking'}<Network size={14} class="shrink-0" />
			{:else if s.id === 'storage'}<Database size={14} class="shrink-0" />
			{:else if s.id === 'catalog'}<Library size={14} class="shrink-0" />
			{:else}<ClipboardList size={14} class="shrink-0" />{/if}
			<span class="truncate">{s.label}</span>
		</a>
	{/each}
</nav>
