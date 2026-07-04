<script lang="ts">
	import { Database, Folder, Library, Network, Server } from 'lucide-svelte';
	import type { Section } from '$lib/nav';

	// vCenter's inventory switcher: five sections, each with its own tree below,
	// all rendering into the same center workspace.
	let { active }: { active: Section } = $props();

	const SECTIONS: { id: Section; label: string; href: string }[] = [
		{ id: 'compute', label: 'Compute', href: '/compute' },
		{ id: 'hosts', label: 'Hosts', href: '/hosts' },
		{ id: 'networking', label: 'Networking', href: '/networking' },
		{ id: 'storage', label: 'Storage', href: '/storage' },
		{ id: 'catalog', label: 'Catalog', href: '/catalog' },
	];
</script>

<nav class="grid grid-cols-5 border-b border-line">
	{#each SECTIONS as s (s.id)}
		<a
			href={s.href}
			title={s.label}
			class="flex flex-col items-center gap-0.5 py-2 text-[10px] {active === s.id
				? 'bg-select font-medium text-accent-ink'
				: 'text-ink-muted hover:bg-select-soft hover:text-ink-soft'}"
		>
			{#if s.id === 'compute'}<Folder size={15} />
			{:else if s.id === 'hosts'}<Server size={15} />
			{:else if s.id === 'networking'}<Network size={15} />
			{:else if s.id === 'storage'}<Database size={15} />
			{:else}<Library size={15} />{/if}
			{s.label}
		</a>
	{/each}
</nav>
