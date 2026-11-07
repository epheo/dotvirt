<script lang="ts">
	import { Book, Database, GitBranch, Monitor, Network, Server } from '@lucide/svelte';
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

<!-- Two rows of three tiles, the label under its icon: every name reads whole
     at the rail's width. -->
<nav class="grid grid-cols-3 gap-1 border-b border-side-line px-2 py-2">
	{#each SECTIONS as s (s.id)}
		<a
			href={s.href}
			aria-current={active === s.id ? 'page' : undefined}
			class="flex flex-col items-center gap-1.5 rounded-lg px-1 py-2 text-sm {active === s.id
				? 'bg-side-active text-side-ink'
				: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
		>
			{#if s.id === 'compute'}<Monitor size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'hosts'}<Server size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'networking'}<Network size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'storage'}<Database size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'catalog'}<Book size={22} strokeWidth={1.75} class="shrink-0" />
			{:else}<GitBranch size={22} strokeWidth={1.75} class="shrink-0" />{/if}
			{s.label}
		</a>
	{/each}
</nav>
