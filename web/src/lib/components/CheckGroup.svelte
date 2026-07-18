<script lang="ts">
	// Multi-select well: checkbox rows in a bounded scroll area. The filter box
	// appears only past the size where scanning beats typing.
	type Item = { value: string; label?: string; hint?: string };

	let {
		items,
		selected = $bindable([]),
		filterAt = 8,
	}: { items: Item[]; selected?: string[]; filterAt?: number } = $props();

	let query = $state('');
	const shown = $derived(
		query
			? items.filter((i) => (i.label ?? i.value).toLowerCase().includes(query.toLowerCase()))
			: items,
	);

	function toggle(v: string, on: boolean) {
		selected = on ? [...selected, v] : selected.filter((s) => s !== v);
	}
</script>

<div class="space-y-1">
	{#if items.length > filterAt}
		<input
			bind:value={query}
			placeholder="Filter…"
			class="w-full rounded border border-line-strong px-2 py-1 text-xs focus:border-accent/60"
		/>
	{/if}
	<div class="max-h-40 space-y-1 overflow-y-auto rounded border border-line-strong p-2">
		{#each shown as item (item.value)}
			<label class="flex items-center gap-2 text-xs">
				<input
					type="checkbox"
					checked={selected.includes(item.value)}
					onchange={(e) => toggle(item.value, e.currentTarget.checked)}
				/>
				<span class="text-ink-soft">{item.label ?? item.value}</span>
				{#if item.hint}
					<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
						>{item.hint}</span
					>
				{/if}
			</label>
		{:else}
			<p class="text-xs text-ink-faint">No matches.</p>
		{/each}
	</div>
</div>
