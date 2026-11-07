<script lang="ts">
	import type { Snippet } from 'svelte';

	// One consequence of an action, said before it runs: a dot for how it
	// lands, then the sentence. Stack rows in a flex column with gap-1.5. sm is
	// the line under a single action; md is a list in a sheet or panel.
	let {
		tone = 'none',
		size = 'md',
		children,
	}: {
		tone?: 'ok' | 'warn' | 'danger' | 'info' | 'none';
		size?: 'md' | 'sm';
		children: Snippet;
	} = $props();

	const DOT = {
		ok: 'bg-ok',
		warn: 'bg-warn',
		danger: 'bg-danger',
		info: 'bg-accent',
		none: 'bg-line-strong',
	};
</script>

<div
	class="flex items-start gap-2 text-ink-soft {size === 'sm'
		? 'text-xs leading-4'
		: 'text-[13px] leading-[18px]'}"
>
	<!-- The dot box is one line tall, so the dot centers on the first line. -->
	<span class="flex shrink-0 items-center {size === 'sm' ? 'h-4' : 'h-[18px]'}" aria-hidden="true">
		<span class="size-[7px] rounded-full {DOT[tone]}"></span>
	</span>
	<span class="min-w-0">{@render children()}</span>
</div>
