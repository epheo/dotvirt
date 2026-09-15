<script lang="ts">
	import type { Snippet } from 'svelte';

	// The review pane's frame, shared by a staged item, a pull request and a
	// past commit: the header row (icon, title, whatever the review adds after
	// it), the body, and the footer bar that carries the review's one action.
	let {
		title,
		icon,
		head,
		footer,
		children,
	}: { title: string; icon: Snippet; head?: Snippet; footer: Snippet; children: Snippet } =
		$props();
</script>

<div class="flex items-center gap-2.5 border-b border-line px-4 py-3">
	{@render icon()}
	<span class="min-w-0 truncate text-[15px] font-semibold text-ink">{title}</span>
	{@render head?.()}
</div>
<div class="space-y-3 p-4">
	{@render children()}
</div>
<div class="mt-auto border-t border-line bg-inset px-4 py-3">
	{@render footer()}
</div>
