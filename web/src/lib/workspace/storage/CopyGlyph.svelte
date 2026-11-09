<script lang="ts">
	import type { CellKind } from '#lib/replication.js';

	// One replica's mark. Filled is a copy that can serve, an outline is one
	// that cannot yet or no longer, the diamond votes but holds no data.
	let {
		kind,
		percent = 0,
		label = '',
		down = false,
	}: { kind: CellKind; percent?: number; label?: string; down?: boolean } = $props();

	const BOX = 'relative block size-3.5 overflow-hidden rounded-[3px] box-border';
</script>

{#if kind}
	<span
		class="inline-flex size-4 items-center justify-center"
		title={label}
		role="img"
		aria-label={label}
	>
		{#if kind === 'copy'}
			<span class="{BOX} {down ? 'border-[1.5px] border-danger bg-danger-soft' : 'bg-glyph'}"
			></span>
		{:else if kind === 'catching'}
			<span class="{BOX} border-[1.5px] border-glyph-2">
				<span class="absolute inset-x-0 bottom-0 bg-glyph-2" style:height="{percent}%"></span>
			</span>
		{:else if kind === 'behind'}
			<span class="{BOX} border-[1.5px] border-warn bg-warn-soft"></span>
		{:else if kind === 'lost'}
			<span class="{BOX} border-[1.5px] border-dashed border-danger"></span>
		{:else if kind === 'pending'}
			<span class="{BOX} border-[1.5px] border-dashed border-line-strong"></span>
		{:else if kind === 'remote'}
			<span class="{BOX} border-[1.5px] border-dashed border-accent"></span>
		{:else}
			<span
				class="block size-2.5 rotate-45 rounded-[1px] {kind === 'witness-lost' || down
					? 'bg-danger'
					: 'bg-ink-faint'}"
			></span>
		{/if}
	</span>
{/if}
