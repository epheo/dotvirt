<script lang="ts">
	import type { Snippet } from 'svelte';

	// The read-only info card: bordered section, uppercase header, optional
	// header action (e.g. a configure tab's Edit link). The body is the
	// caller's — typically a <dl> of <Row>s, or a list.
	let { title, action, children }: { title: string; action?: Snippet; children: Snippet } =
		$props();
</script>

<section class="rounded border border-slate-200">
	<div class="flex items-center justify-between border-b border-slate-200 bg-slate-50 px-3 py-1.5">
		<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">{title}</h3>
		{#if action}{@render action()}{/if}
	</div>
	{@render children()}
</section>
