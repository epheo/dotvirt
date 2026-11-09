<script lang="ts">
	import { Database, GitBranch, Monitor, Network, Server } from '@lucide/svelte';
	import { clusterGate } from '#lib/gates.js';
	import type { Section } from '#lib/nav.js';

	// The section switcher: each section has its own tree below, all rendering
	// into the same center workspace. Work in flight lives in the change tray,
	// not on the switcher. Hosts shows on a known yes only: an unread or failed
	// capability answer offers no tile.
	let { active }: { active: Section } = $props();

	const tiles = $derived(
		(
			[
				{ id: 'compute', label: 'Compute', href: '/compute' },
				{ id: 'hosts', label: 'Hosts', href: '/hosts' },
				{ id: 'networking', label: 'Networking', href: '/networking' },
				{ id: 'storage', label: 'Storage', href: '/storage' },
				{ id: 'changes', label: 'Changes', href: '/changes' },
			] as { id: Section; label: string; href: string }[]
		).filter((s) => s.id !== 'hosts' || clusterGate.hosts().ok),
	);
	// Five tiles read three over two, four read two over two: every row fills
	// the rail.
	const span = (i: number) => (tiles.length === 5 && i < 3 ? 'col-span-2' : 'col-span-3');
</script>

<!-- The label under its icon: every name reads whole at the rail's width. -->
<nav class="grid grid-cols-6 gap-1 border-b border-side-line px-2 py-2">
	{#each tiles as s, i (s.id)}
		<a
			href={s.href}
			aria-current={active === s.id ? 'page' : undefined}
			class="{span(i)} flex flex-col items-center gap-1.5 rounded-lg px-1 py-2 text-sm {active ===
			s.id
				? 'bg-side-active text-side-ink'
				: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
		>
			{#if s.id === 'compute'}<Monitor size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'hosts'}<Server size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'networking'}<Network size={22} strokeWidth={1.75} class="shrink-0" />
			{:else if s.id === 'storage'}<Database size={22} strokeWidth={1.75} class="shrink-0" />
			{:else}<GitBranch size={22} strokeWidth={1.75} class="shrink-0" />{/if}
			{s.label}
		</a>
	{/each}
</nav>
