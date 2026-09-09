<script lang="ts">
	import { ClipboardList, Database, Folder, Library, Network, Server } from 'lucide-svelte';
	import type { Section } from '$lib/nav';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';

	// The section switcher: six sections, each with its own tree below, all
	// rendering into the same center workspace. Changes carries a dot when the
	// caller has work in flight (staged edits, own open PRs); the header's
	// Propose button carries the count, so the number lives once.
	let { active }: { active: Section } = $props();

	const SECTIONS: { id: Section; label: string; href: string }[] = [
		{ id: 'compute', label: 'Compute', href: '/compute' },
		{ id: 'hosts', label: 'Hosts', href: '/hosts' },
		{ id: 'networking', label: 'Networking', href: '/networking' },
		{ id: 'storage', label: 'Storage', href: '/storage' },
		{ id: 'catalog', label: 'Catalog', href: '/catalog' },
		{ id: 'changes', label: 'Changes', href: '/changes' },
	];
	const inFlight = $derived(drafts.count > 0 || inventory.proposals.some((p) => p.mine));
</script>

<nav class="grid grid-cols-6 border-b border-side-line">
	{#each SECTIONS as s (s.id)}
		<a
			href={s.href}
			title={s.label}
			class="relative flex flex-col items-center gap-0.5 py-2 text-[10px] {active === s.id
				? 'bg-side-active font-medium text-side-ink shadow-[inset_0_2px_0_var(--color-accent-hover)]'
				: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
		>
			{#if s.id === 'compute'}<Folder size={15} />
			{:else if s.id === 'hosts'}<Server size={15} />
			{:else if s.id === 'networking'}<Network size={15} />
			{:else if s.id === 'storage'}<Database size={15} />
			{:else if s.id === 'catalog'}<Library size={15} />
			{:else}<ClipboardList size={15} />{/if}
			{s.label}
			{#if s.id === 'changes' && inFlight}
				<span
					class="absolute top-1.5 right-2 h-1.5 w-1.5 rounded-full bg-accent-hover"
					title="Changes in flight"
				></span>
			{/if}
		</a>
	{/each}
</nav>
