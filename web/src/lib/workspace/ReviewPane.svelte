<script lang="ts">
	import type { Snippet } from 'svelte';

	// The review card beside the list, shared by a staged item, a pull request
	// and a past commit: the name (mono for an object), a line saying where it
	// stands, ReviewSection blocks, and the review's one action at the foot.
	// head sits right of the name (pills, a forge link).
	let {
		title,
		mono = false,
		sub,
		head,
		footer,
		children,
	}: {
		title: string;
		mono?: boolean;
		sub?: Snippet;
		head?: Snippet;
		footer?: Snippet;
		children: Snippet;
	} = $props();

	const id = $props.id();
</script>

<section
	aria-labelledby={id}
	class="flex min-w-0 flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
>
	<div
		class="flex flex-wrap items-start gap-x-3 gap-y-1.5 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
	>
		<div class="min-w-0 flex-1">
			<h2
				{id}
				class="text-base leading-6 break-words text-ink {mono
					? 'font-mono font-medium'
					: 'font-bold'}"
			>
				{title}
			</h2>
			{#if sub}<p class="text-[13px] leading-5 text-ink-muted">{@render sub()}</p>{/if}
		</div>
		{#if head}<div class="flex flex-wrap items-center gap-2">{@render head()}</div>{/if}
	</div>
	<div class="flex flex-col divide-y divide-line-soft">
		{@render children()}
	</div>
	{#if footer}
		<div class="flex flex-col gap-2 border-t border-line-soft px-card py-3.5">
			{@render footer()}
		</div>
	{/if}
</section>
