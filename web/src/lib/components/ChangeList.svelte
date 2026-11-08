<script lang="ts">
	import type { Change } from '#lib/api.js';
	// marks: also name the changes that apply without a restart, where a VM
	// edit mixes both. pills: each change ends in a right-aligned Restart or
	// Live pill, the review sheet's reading.
	let {
		changes,
		marks = false,
		pills = false,
	}: { changes: Change[]; marks?: boolean; pills?: boolean } = $props();
</script>

<ul class="space-y-0.5 text-[13px]">
	{#each changes as c (c.field + c.action + (c.to ?? '') + (c.from ?? ''))}
		<li class="flex items-baseline gap-2">
			<span class="w-32 shrink-0 text-ink-muted">{c.field}</span>
			{#if c.action === 'change'}
				<span class="text-ink-faint line-through">{c.from || 'none'}</span>
				<span class="text-ink-faint">to</span>
				<span class="font-medium text-ink">{c.to}</span>
			{:else if c.action === 'add'}
				<span class="font-medium text-ok-ink">+ {c.to}</span>
			{:else}
				<span class="font-medium text-danger-ink line-through">- {c.from}</span>
			{/if}
			{#if pills}
				<span
					class="ml-auto shrink-0 rounded-full px-2 text-xs {c.restart
						? 'bg-warn-soft text-warn-ink'
						: 'bg-ok-soft text-ok-ink'}"
					title={c.restart ? "Applies at the VM's next power cycle" : 'Applies to the running VM'}
					>{c.restart ? 'Restart' : 'Live'}</span
				>
			{:else if c.restart}
				<span
					class="rounded bg-warn-soft px-1 text-[10px] text-warn-ink"
					title="Applies at the VM's next power cycle">restart</span
				>
			{:else if marks}
				<span class="text-[10px] text-ink-muted">no restart</span>
			{/if}
		</li>
	{/each}
	{#if changes.length === 0}
		<li class="text-xs text-ink-faint italic">no changes</li>
	{/if}
</ul>
