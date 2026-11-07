<script lang="ts">
	import { ArrowUpRight, Ban, ShieldCheck } from '@lucide/svelte';
	import type { RuleLine } from '#lib/rulelines.js';
	import { inventory } from '#lib/state/inventory.svelte.js';

	// Rules as sentences: an icon for what the rule does, the sentence, then
	// where it comes from in small print with the rule's name linking to the
	// Firewall tab. owner adds the project for rules from another namespace.
	let {
		lines,
		owner = false,
		gap = 'gap-2.5',
	}: { lines: RuleLine[]; owner?: boolean; gap?: string } = $props();

	const firewallHref = (name: string) => `/networking?tab=firewall&q=${encodeURIComponent(name)}`;
</script>

<ul class="flex flex-col {gap}">
	{#each lines as l (l.key)}
		<li class="grid grid-cols-[16px_minmax(0,1fr)] gap-x-2.5 text-[13px] leading-5 text-ink-soft">
			<span class="mt-0.5" aria-hidden="true">
				{#if l.icon === 'allow'}<ShieldCheck size={16} class="text-ok" />
				{:else if l.icon === 'out'}<ArrowUpRight size={16} class="text-ink-faint" />
				{:else}<Ban size={16} class="text-ink-faint" />{/if}
			</span>
			<span>{l.text}</span>
			<small class="col-start-2 text-xs leading-4 text-ink-faint">
				{l.tier}{#if l.policy}{' '}<a
						href={firewallHref(l.policy.name)}
						class="text-accent-ink hover:underline">{l.policy.name}</a
					>{/if}{#if l.note}, {l.note}{/if}{#if owner && l.policy?.namespace}, {inventory.projectOf(
						l.policy.namespace,
					) || l.policy.namespace}{/if}
			</small>
		</li>
	{/each}
</ul>
