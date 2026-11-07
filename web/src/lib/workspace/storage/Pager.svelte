<script lang="ts">
	import { ChevronLeft, ChevronRight } from '@lucide/svelte';
	import { PAGE_SIZES, num, type Page } from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';

	// The foot of a paged table: where the rows sit in the whole, the page
	// size, and the page steps.
	let {
		page,
		onpage,
		onper,
		per,
	}: {
		page: Page<unknown>;
		per: number;
		onpage: (n: number) => void;
		onper: (n: number) => void;
	} = $props();
	const id = $props.id();
</script>

<div
	class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-line px-card py-3 text-[13px] text-ink-soft"
>
	<span>
		{#if page.total}
			Showing <strong>{page.from}-{page.to}</strong> of {num(page.total)}
		{:else}
			Showing 0 of 0
		{/if}
	</span>
	<span class="ml-auto"></span>
	<label for={id} class="text-ink-faint">Rows per page</label>
	<select
		{id}
		value={per}
		onchange={(e) => onper(Number(e.currentTarget.value))}
		class="h-[30px] rounded-md border border-line-strong bg-panel px-2 text-[13px] text-ink"
	>
		{#each PAGE_SIZES as n (n)}<option value={n}>{n}</option>{/each}
	</select>
	<Button
		variant="secondary"
		size="sm"
		class="w-[30px] !px-0"
		aria-label="Previous page"
		disabled={page.page <= 1}
		onclick={() => onpage(page.page - 1)}><ChevronLeft size={16} /></Button
	>
	<span>Page {page.page} of {page.pages}</span>
	<Button
		variant="secondary"
		size="sm"
		class="w-[30px] !px-0"
		aria-label="Next page"
		disabled={page.page >= page.pages}
		onclick={() => onpage(page.page + 1)}><ChevronRight size={16} /></Button
	>
</div>
