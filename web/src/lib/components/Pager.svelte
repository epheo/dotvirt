<script lang="ts">
	import { ChevronLeft, ChevronRight } from '@lucide/svelte';
	import { DEFAULT_PAGE_SIZE, PAGE_SIZES, pageSlice, rangeLabel, totalLabel } from '#lib/paging.js';

	// The footer of a paged table: where the page sits ("Showing 1-50 of 389"),
	// rows per page, and prev / next. Below the smallest page size there is
	// nothing to page, so only the count shows. The host slices its rows with
	// pageSlice(total, page, size) and owns when page resets. class is for
	// placement only (a top border, padding).
	let {
		total,
		page = $bindable(1),
		size = $bindable(DEFAULT_PAGE_SIZE),
		sizes = PAGE_SIZES,
		class: cls = '',
	}: {
		total: number;
		page?: number;
		size?: number;
		sizes?: readonly number[];
		class?: string;
	} = $props();

	const s = $derived(pageSlice(total, page, size));
	const paged = $derived(total > Math.min(...sizes));
	const id = $props.id();

	const NAV =
		'inline-flex size-7 items-center justify-center rounded-md border border-line bg-panel text-ink hover:border-ink-faint disabled:opacity-50 disabled:hover:border-line';
</script>

<div
	class="flex flex-wrap items-center gap-x-3 gap-y-2 text-[13px] text-ink-soft {cls}"
	role="navigation"
	aria-label="Pages"
>
	<span>Showing <strong class="font-bold text-ink">{rangeLabel(s)}</strong> of {totalLabel(s)}</span
	>
	{#if paged}
		<span class="flex-1"></span>
		<label for={id} class="text-ink-faint">Rows per page</label>
		<select
			{id}
			value={size}
			onchange={(e) => {
				size = Number(e.currentTarget.value);
				page = 1;
			}}
			class="h-7 rounded-md border border-line-strong bg-panel px-2 text-[13px] text-ink"
		>
			{#each sizes as n (n)}<option value={n}>{n}</option>{/each}
		</select>
		<button
			type="button"
			class={NAV}
			disabled={s.page <= 1}
			onclick={() => (page = s.page - 1)}
			aria-label="Previous page"><ChevronLeft size={16} /></button
		>
		<span>Page {s.page} of {s.pages}</span>
		<button
			type="button"
			class={NAV}
			disabled={s.page >= s.pages}
			onclick={() => (page = s.page + 1)}
			aria-label="Next page"><ChevronRight size={16} /></button
		>
	{/if}
</div>
