<script lang="ts">
	import { ChevronDown, Search } from '@lucide/svelte';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { FIELD } from './sheet.svelte.js';

	// Where a create lands, as "project / namespace": a combobox that searches
	// at scale, the opener's project first. A project with one namespace reads
	// as the project alone when short is set.
	let {
		id,
		value = $bindable(),
		namespaces,
		first = '',
		short = false,
	}: {
		id: string;
		value: string;
		namespaces: string[];
		first?: string; // the project listed first
		short?: boolean;
	} = $props();

	let open = $state(false);
	let query = $state('');
	let active = $state(0);
	let root = $state<HTMLDivElement>();

	const places = $derived(
		namespaces
			.map((ns) => ({ ns, project: inventory.projectOf(ns) || ns }))
			.sort(
				(a, b) =>
					Number(b.project === first) - Number(a.project === first) ||
					a.project.localeCompare(b.project) ||
					a.ns.localeCompare(b.ns),
			),
	);
	const sole = (project: string) => places.filter((p) => p.project === project).length === 1;
	const label = (p: { ns: string; project: string }) =>
		short && sole(p.project) ? p.project : `${p.project} / ${p.ns}`;
	const shown = $derived(
		places.filter((p) => !query || label(p).toLowerCase().includes(query.trim().toLowerCase())),
	);
	const current = $derived(places.find((p) => p.ns === value));

	function choose(ns: string) {
		value = ns;
		open = false;
		query = '';
		root?.querySelector<HTMLButtonElement>('button[aria-haspopup]')?.focus();
	}

	function keys(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			open = false;
			root?.querySelector<HTMLButtonElement>('button[aria-haspopup]')?.focus();
		} else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
			e.preventDefault();
			const n = Math.min(shown.length, 50);
			if (n) active = (active + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
		} else if (e.key === 'Enter' && shown[active]) {
			e.preventDefault();
			choose(shown[active].ns);
		}
	}

	$effect(() => {
		if (!open) return;
		const away = (e: PointerEvent) => {
			if (root && !root.contains(e.target as Node)) open = false;
		};
		document.addEventListener('pointerdown', away);
		return () => document.removeEventListener('pointerdown', away);
	});
</script>

<div class="relative" bind:this={root}>
	<button
		{id}
		type="button"
		aria-haspopup="listbox"
		aria-expanded={open}
		onclick={() => {
			open = !open;
			active = 0;
		}}
		class="{FIELD} flex items-center justify-between gap-2 text-left"
	>
		<span class="min-w-0 truncate font-mono font-medium">
			{#if current}{label(current)}{:else}<span class="font-sans font-normal text-ink-faint"
					>Pick a place</span
				>{/if}
		</span>
		<ChevronDown size={16} class="shrink-0 text-ink-muted" />
	</button>
	{#if open}
		<div
			class="absolute inset-x-0 top-10 z-20 flex flex-col rounded-md border border-line bg-panel shadow-lg"
			role="presentation"
			onkeydown={keys}
		>
			<div class="flex items-center gap-2 border-b border-line-soft px-2.5 text-ink-muted">
				<Search size={14} class="shrink-0" />
				<input
					type="search"
					bind:value={query}
					oninput={() => (active = 0)}
					aria-label="Find a project or namespace"
					aria-controls="{id}-list"
					class="h-9 min-w-0 flex-1 border-0 bg-transparent text-sm text-ink outline-none"
					autocomplete="off"
					{@attach (el) => el.focus()}
				/>
			</div>
			<ul id="{id}-list" role="listbox" class="max-h-64 overflow-y-auto py-1">
				{#each shown.slice(0, 50) as p, i (p.ns)}
					<li
						role="option"
						aria-selected={p.ns === value}
						class="flex cursor-pointer items-center justify-between gap-2 px-3 py-1.5 font-mono text-[13px] {i ===
						active
							? 'bg-select-soft'
							: ''} {p.ns === value ? 'font-medium text-accent-ink' : 'text-ink'}"
						onpointerdown={(e) => {
							e.preventDefault();
							choose(p.ns);
						}}
						onpointerenter={() => (active = i)}
					>
						{label(p)}
						{#if p.project === first}<span class="font-sans text-xs text-ink-faint">yours</span
							>{/if}
					</li>
				{:else}
					<li class="px-3 py-1.5 text-[13px] text-ink-faint">Nothing matches.</li>
				{/each}
			</ul>
			{#if shown.length > 50}
				<p class="border-t border-line-soft px-3 py-1.5 text-xs text-ink-faint">
					{shown.length - 50} more. Keep typing to narrow the list.
				</p>
			{/if}
		</div>
	{/if}
</div>
