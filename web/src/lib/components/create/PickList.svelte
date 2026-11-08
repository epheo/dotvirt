<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';
	import { Search } from '@lucide/svelte';

	// A picker that holds up at scale: a search box, a count of what matches,
	// and at most `limit` rows (the rest are reached by searching). multiple
	// renders checkboxes, else radios. done, when given, adds the Done button
	// beside the search box (the editor that hosts the list closes with it).
	let {
		label,
		query = $bindable(''),
		items,
		key,
		selected,
		multiple = false,
		disabled,
		ontoggle,
		note,
		row,
		after,
		done,
		limit = 50,
		search = true,
	}: {
		label: string;
		query?: string;
		items: T[];
		key: (t: T) => string;
		selected: (t: T) => boolean;
		multiple?: boolean;
		disabled?: (t: T) => boolean;
		ontoggle: (t: T, on: boolean) => void;
		note?: string;
		row: Snippet<[T]>;
		after?: Snippet;
		done?: () => void;
		limit?: number;
		search?: boolean;
	} = $props();

	const id = $props.id();
	const shown = $derived(items.slice(0, limit));
</script>

<div class="flex min-w-0 flex-col gap-2">
	{#if search || done}
		<div class="flex gap-2">
			{#if search}
				<div
					class="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-md border border-line-strong bg-panel px-2.5 text-ink-muted focus-within:border-accent focus-within:ring-1 focus-within:ring-accent"
				>
					<Search size={16} class="shrink-0" />
					<label class="sr-only" for="{id}-q">{label}</label>
					<input
						id="{id}-q"
						type="search"
						bind:value={query}
						class="h-full min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-none"
						autocomplete="off"
					/>
				</div>
			{/if}
			{#if done}
				<button
					type="button"
					onclick={done}
					class="h-9 rounded-md border border-line bg-panel px-3 text-sm text-ink hover:border-ink-faint"
					>Done</button
				>
			{/if}
		</div>
	{/if}
	{#if note}<p class="text-xs leading-4 text-ink-faint" aria-live="polite">{note}</p>{/if}
	{#if shown.length}
		<div
			class="flex max-h-72 flex-col overflow-y-auto rounded-md border border-line"
			role={multiple ? 'group' : 'radiogroup'}
			aria-label={label}
		>
			{#each shown as t (key(t))}
				{@const on = selected(t)}
				{@const off = disabled?.(t) ?? false}
				<label
					class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] items-start gap-3 border-t border-line-soft px-3 py-2 first:border-t-0 {on
						? 'bg-select-soft'
						: 'hover:bg-inset'} {off ? 'cursor-not-allowed opacity-60' : ''}"
				>
					<input
						type={multiple ? 'checkbox' : 'radio'}
						name={multiple ? undefined : id}
						checked={on}
						disabled={off}
						onchange={(e) => ontoggle(t, e.currentTarget.checked)}
						class="mt-0.5 size-4 accent-accent"
					/>
					<span class="min-w-0">{@render row(t)}</span>
				</label>
			{/each}
		</div>
		{#if items.length > shown.length}
			<p class="text-xs text-ink-faint">
				{items.length - shown.length} more. Search to narrow the list.
			</p>
		{/if}
	{/if}
	{#if after}{@render after()}{/if}
</div>
