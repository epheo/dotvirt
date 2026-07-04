<script lang="ts">
	import type { Change } from '$lib/api';
	let { changes }: { changes: Change[] } = $props();
</script>

<ul class="space-y-0.5 text-[13px]">
	{#each changes as c (c.field + c.action + (c.to ?? '') + (c.from ?? ''))}
		<li class="flex items-baseline gap-2">
			<span class="w-32 shrink-0 text-ink-muted">{c.field}</span>
			{#if c.action === 'change'}
				<span class="text-ink-faint line-through">{c.from || '∅'}</span>
				<span class="text-ink-faint">→</span>
				<span class="font-medium text-ink">{c.to}</span>
			{:else if c.action === 'add'}
				<span class="font-medium text-green-700">+ {c.to}</span>
			{:else}
				<span class="font-medium text-red-700 line-through">− {c.from}</span>
			{/if}
		</li>
	{/each}
	{#if changes.length === 0}
		<li class="text-xs text-ink-faint italic">no changes</li>
	{/if}
</ul>
