<script lang="ts">
	import { Database, Folder, Library, Network, Server } from 'lucide-svelte';
	import type { Section } from '$lib/nav';

	// The inventory switcher: five sections, each with its own tree below,
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

<nav class="grid grid-cols-5 border-b border-side-line">
	{#each SECTIONS as s (s.id)}
		<a
			href={s.href}
			title={s.label}
			class="flex flex-col items-center gap-0.5 py-2 text-[10px] {active === s.id
				? 'bg-side-active font-medium text-side-ink shadow-[inset_0_2px_0_var(--color-accent-hover)]'
				: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
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
