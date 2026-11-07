<script lang="ts">
	import Button from '#lib/components/Button.svelte';

	// The footer of a paged list: where the page sits in the whole, and the
	// two steps. Pages are 50 rows unless the host says otherwise.
	let {
		total,
		page = $bindable(0),
		size = 50,
		noun,
	}: { total: number; page?: number; size?: number; noun: string } = $props();

	const pages = $derived(Math.max(1, Math.ceil(total / size)));
	const from = $derived(total ? page * size + 1 : 0);
	const to = $derived(Math.min(total, (page + 1) * size));
	const fmt = (n: number) => n.toLocaleString('en-US');
	$effect(() => {
		if (page > pages - 1) page = pages - 1;
	});
</script>

<div class="flex flex-wrap items-center gap-3 border-t border-line-soft px-card py-2.5 text-[13px]">
	<span class="text-ink-faint">
		{#if total}Showing {fmt(from)}-{fmt(to)} of {fmt(total)} {noun}{:else}No {noun}{/if}
	</span>
	{#if pages > 1}
		<span class="ml-auto flex gap-2">
			<Button variant="secondary" size="sm" disabled={page === 0} onclick={() => page--}
				>Previous</Button
			>
			<Button variant="secondary" size="sm" disabled={page >= pages - 1} onclick={() => page++}
				>Next</Button
			>
		</span>
	{/if}
</div>
