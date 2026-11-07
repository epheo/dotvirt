<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { ChevronDown, ChevronUp } from '@lucide/svelte';
	import { vmKey, type DraftItem, type Options, type VM } from '#lib/api.js';
	import { adoptVM } from '#lib/actions.js';
	import { quantity, quantityBytes } from '#lib/format.js';
	import type { Issue } from '#lib/issues.js';
	import { vmSizing } from '#lib/sizing.js';
	import { TONE_DOT, TONE_PILL, vmState, vmStateRank, type VMStateView } from '#lib/status.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { persisted } from '#lib/state/persisted.svelte.js';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { TBODY, THEAD_TR } from '#lib/table.js';
	import { vmHref } from '#lib/nav.js';
	import StagedBadge from './StagedBadge.svelte';
	import StatusPill from './StatusPill.svelte';
	import TextInput from './TextInput.svelte';

	let {
		vms,
		onselect,
		selected = $bindable(new Set<string>()),
		staged,
		onstagedopen,
		oncontextvm,
		activeKey = null,
	}: {
		vms: VM[];
		onselect: (vm: VM) => void;
		selected?: Set<string>;
		staged: Map<string, DraftItem>;
		onstagedopen: (vm: VM) => void;
		oncontextvm?: (vm: VM, x: number, y: number) => void;
		// "ns/name" of the row the side peek is inspecting (the selection blue).
		activeKey?: string | null;
	} = $props();

	// Sizing once per VM object, not per sort comparison: inventory.apply keeps
	// unchanged VMs identical across frames, so the cache survives live updates.
	type RowSize = { cpu?: number; memory?: string; memBytes: number };
	let sizes = new WeakMap<VM, RowSize>();
	let sizesFor: Options | null = null;
	function sizeOf(vm: VM): RowSize {
		const options = inventory.options;
		if (options !== sizesFor) {
			sizes = new WeakMap();
			sizesFor = options;
		}
		let s = sizes.get(vm);
		if (!s) {
			const { cpu, memory } = vmSizing(vm, options);
			s = { cpu, memory, memBytes: (memory && quantityBytes(memory)) || 0 };
			sizes.set(vm, s);
		}
		return s;
	}

	// The issues plane, per VM: the header's count and the "With issues" chip
	// read the same list, so the two numbers can never disagree. A firing alert
	// on a VM that is otherwise fine becomes its State exception.
	const issueOf = $derived.by(() => {
		const m = new Map<string, Issue>();
		for (const i of inventory.issues) if (!m.has(i.scope)) m.set(i.scope, i);
		return m;
	});

	// Open PRs whose diff names a VM, by VM: the "In a change" column shows
	// other people's work beside this user's staged edit. A PR's items load
	// once per PR; the load re-runs only when the set of open PRs moves.
	$effect(() => {
		inventory.proposalsKey;
		untrack(() => reviewCache.loadProposals(inventory.proposals));
	});
	const prsOf = $derived(reviewCache.vmPRs(inventory.proposals));

	// What a person does next about a VM: the fix its issue offers, or bringing
	// it under git. A fix that is the VM's own page reads as the reason to go.
	type Step = { label: string; run: () => void };
	function nextStep(vm: VM): Step | null {
		const i = issueOf.get(vmKey(vm));
		if (i) {
			const a = i.action;
			if ('href' in a)
				return {
					label: a.href === vmHref(vm.namespace, vm.name) ? 'See why' : a.label,
					run: () => goto(a.href),
				};
			if ('modal' in a) return { label: a.label, run: () => (ui.modal = a.modal) };
			return { label: a.label, run: () => void adoptVM(vm) };
		}
		if (vm.sync === 'NotTracked' && inventory.canAuthor(vm.namespace))
			return { label: 'Adopt into git', run: () => void adoptVM(vm) };
		return null;
	}

	function stateOf(vm: VM): VMStateView {
		const v = vmState(vm);
		if (v.exception) return v;
		const i = issueOf.get(vmKey(vm));
		return i ? { ...v, exception: { tone: i.severity, label: i.label, hint: i.detail } } : v;
	}

	let search = $state('');

	type SortKey = 'name' | 'namespace' | 'state' | 'nodeName' | 'cpuCores' | 'memory' | 'guestIP';
	type Filter = 'all' | 'issues' | 'running' | 'stopped' | 'staged';

	// Sort + filter survive reloads; the search box is transient on purpose.
	// The default sort puts what needs a person on top.
	const prefs = persisted<{ sortKey: SortKey; sortDir: 1 | -1; filter: Filter }>('dotvirt.vmgrid', {
		sortKey: 'state',
		sortDir: 1,
		filter: 'all',
	});

	function setSort(k: SortKey) {
		prefs.value =
			prefs.value.sortKey === k
				? { ...prefs.value, sortDir: prefs.value.sortDir === 1 ? -1 : 1 }
				: { ...prefs.value, sortKey: k, sortDir: 1 };
	}

	function cmp(a: VM, b: VM): number {
		const key = prefs.value.sortKey;
		switch (key) {
			case 'state':
				// Issues first, as their own group, then by how bad the state is.
				return (
					Number(issueOf.has(vmKey(b))) - Number(issueOf.has(vmKey(a))) ||
					vmStateRank(stateOf(b)) - vmStateRank(stateOf(a))
				);
			case 'cpuCores':
				return (sizeOf(a).cpu ?? 0) - (sizeOf(b).cpu ?? 0);
			case 'memory':
				return sizeOf(a).memBytes - sizeOf(b).memBytes;
			default: {
				const av = (a[key] ?? '').toString().toLowerCase();
				const bv = (b[key] ?? '').toString().toLowerCase();
				return av < bv ? -1 : av > bv ? 1 : 0;
			}
		}
	}

	// A healthy row stays quiet; its git relation is one hover away.
	const SYNC_TITLE: Record<string, string> = {
		Synced: 'In sync with git',
		OutOfSync: 'Differs from git',
		NotTracked: 'Not in git',
		Pending: 'Waiting for its first sync',
		Unknown: 'Sync state unknown',
	};

	const stopped = (vm: VM) => vm.phase === 'Stopped' || (!vm.phase && vm.power === 'Off');
	const MATCH: Record<Filter, (vm: VM) => boolean> = {
		all: () => true,
		issues: (vm) => issueOf.has(vmKey(vm)),
		running: (vm) => vm.phase === 'Running',
		stopped,
		staged: (vm) => staged.has(vmKey(vm)) || prsOf.has(vmKey(vm)),
	};
	const FILTERS: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'All' },
		{ id: 'issues', label: 'With issues' },
		{ id: 'running', label: 'Running' },
		{ id: 'stopped', label: 'Stopped' },
		{ id: 'staged', label: 'In a change' },
	];
	const counts = $derived(
		Object.fromEntries(FILTERS.map((f) => [f.id, vms.filter(MATCH[f.id]).length])) as Record<
			Filter,
			number
		>,
	);

	const rows = $derived.by(() => {
		const q = search.trim().toLowerCase();
		const { filter, sortDir } = prefs.value;
		const filtered = vms.filter((vm) => {
			if (!MATCH[filter](vm)) return false;
			if (q) {
				const hay = `${vm.name} ${vm.namespace} ${vm.guestIP ?? ''} ${vm.nodeName ?? ''}`;
				if (!hay.toLowerCase().includes(q)) return false;
			}
			return true;
		});
		// Stable tiebreak on name so equal sort keys don't jitter on live updates.
		return filtered.sort((a, b) => {
			const c = cmp(a, b) * sortDir;
			return c !== 0 ? c : a.name.localeCompare(b.name);
		});
	});

	// Under the default sort, issue rows sit under their own heading.
	const groups = $derived.by(() => {
		if (prefs.value.sortKey !== 'state' || prefs.value.sortDir !== 1) return null;
		const n = rows.filter((vm) => issueOf.has(vmKey(vm))).length;
		return n > 0 && n < rows.length ? { issues: n, rest: rows.length - n } : null;
	});

	const cols: { key: SortKey; label: string; class?: string }[] = [
		{ key: 'name', label: 'Name' },
		{ key: 'namespace', label: 'Namespace' },
		{ key: 'state', label: 'State' },
		{ key: 'nodeName', label: 'Host' },
		{ key: 'cpuCores', label: 'vCPU', class: 'text-right' },
		{ key: 'memory', label: 'Memory', class: 'text-right' },
		{ key: 'guestIP', label: 'IP' },
	];

	const allSelected = $derived(rows.length > 0 && rows.every((vm) => selected.has(vmKey(vm))));
	const someSelected = $derived(rows.some((vm) => selected.has(vmKey(vm))) && !allSelected);

	function toggleOne(vm: VM) {
		const k = vmKey(vm);
		const next = new Set(selected);
		next.has(k) ? next.delete(k) : next.add(k);
		selected = next;
	}

	function toggleAll() {
		const next = new Set(selected);
		if (allSelected) rows.forEach((vm) => next.delete(vmKey(vm)));
		else rows.forEach((vm) => next.add(vmKey(vm)));
		selected = next;
	}

	// Roving tabindex: one row is in the tab order, arrows move focus between
	// rows. Falls back to the peeked row, then the first, when the focused VM
	// leaves the filtered set.
	let focusKey = $state<string | null>(null);
	const tabKey = $derived.by(() => {
		const keys = new Set(rows.map(vmKey));
		if (focusKey && keys.has(focusKey)) return focusKey;
		if (activeKey && keys.has(activeKey)) return activeKey;
		return rows.length ? vmKey(rows[0]) : null;
	});

	function onrowkeydown(e: KeyboardEvent, vm: VM) {
		// The checkbox and the name link keep their own keys.
		if (e.target !== e.currentTarget) return;
		const row = e.currentTarget as HTMLElement;
		const body = row.parentElement!;
		// Group headings are rows too; focus skips them.
		const vmRow = (el: Element | null, up: boolean) => {
			while (el && !el.hasAttribute('tabindex'))
				el = up ? el.previousElementSibling : el.nextElementSibling;
			return el;
		};
		const target = {
			ArrowDown: vmRow(row.nextElementSibling, false),
			ArrowUp: vmRow(row.previousElementSibling, true),
			Home: vmRow(body.firstElementChild, false),
			End: vmRow(body.lastElementChild, true),
		}[e.key];
		if (target !== undefined) {
			e.preventDefault();
			(target as HTMLElement | null)?.focus();
		} else if (e.key === 'Enter') {
			// On the peeked row Enter reaches the inspector, which opens the full page.
			if (vmKey(vm) === activeKey) return;
			e.preventDefault();
			e.stopPropagation();
			onselect(vm);
		} else if (e.key === ' ') {
			e.preventDefault();
			toggleOne(vm);
		}
	}

	// Drop selected keys for VMs no longer present (deleted/scoped out), so the
	// action bar count never counts rows the user can't see.
	$effect(() => {
		const present = new Set(vms.map(vmKey));
		if ([...selected].some((k) => !present.has(k))) {
			selected = new Set([...selected].filter((k) => present.has(k)));
		}
	});
</script>

<div class="flex h-full flex-col">
	<div class="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
		<div class="flex flex-wrap gap-1.5" role="group" aria-label="Show">
			{#each FILTERS as f (f.id)}
				{@const on = prefs.value.filter === f.id}
				<button
					type="button"
					aria-pressed={on}
					onclick={() => (prefs.value = { ...prefs.value, filter: f.id })}
					class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium {on
						? 'border-accent bg-select text-ink'
						: 'border-line-strong bg-panel text-ink-soft hover:bg-inset'}"
				>
					{f.label}
					<span
						class="rounded-full px-1.5 text-[11px] tabular-nums {f.id === 'issues' &&
						counts.issues > 0
							? TONE_PILL.danger
							: 'text-ink-muted'}">{counts[f.id]}</span
					>
				</button>
			{/each}
		</div>
		<TextInput
			bind:value={search}
			placeholder="Filter by name, IP or host"
			width="w-60"
			size="sm"
			class="ml-auto"
			aria-label="Filter VMs"
		/>
	</div>

	<div class="min-h-0 flex-1 overflow-auto">
		<table
			class="w-full text-[13px] whitespace-nowrap"
			role="grid"
			aria-multiselectable="true"
			aria-label="VMs"
		>
			<thead class="sticky top-0 bg-inset text-left text-xs text-ink-muted">
				<tr class={THEAD_TR}>
					<th class="w-8 px-3 py-2">
						<input
							type="checkbox"
							checked={allSelected}
							indeterminate={someSelected}
							onchange={toggleAll}
							title="Select all (filtered)"
							aria-label="Select all (filtered)"
							class="cursor-pointer align-middle"
						/>
					</th>
					{#each cols as c (c.key)}
						<th class="px-3 py-2 font-medium {c.class ?? ''}">
							<button
								onclick={() => setSort(c.key)}
								aria-label="Sort by {c.label}"
								class="inline-flex items-center gap-1 hover:text-ink"
							>
								{c.label}
								{#if prefs.value.sortKey === c.key}
									{#if prefs.value.sortDir === 1}<ChevronUp
											size={12}
											class="text-ink-faint"
										/>{:else}<ChevronDown size={12} class="text-ink-faint" />{/if}
								{/if}
							</button>
						</th>
					{/each}
					<th class="px-3 py-2 font-medium">In a change</th>
					<th class="px-3 py-2 font-medium">Next step</th>
				</tr>
			</thead>
			<tbody class={TBODY}>
				{#each rows as vm, i (vm.namespace + '/' + vm.name)}
					{@const k = vmKey(vm)}
					{#if groups && (i === 0 || i === groups.issues)}
						<tr class="border-y border-line bg-inset">
							<td colspan={cols.length + 3} class="px-3 py-1.5 text-xs font-semibold text-ink">
								{i === 0
									? `With issues (${groups.issues}), always on top`
									: `Everything else (${groups.rest})`}
							</td>
						</tr>
					{/if}
					{@const sc = staged.get(k)}
					{@const size = sizeOf(vm)}
					{@const st = stateOf(vm)}
					{@const next = nextStep(vm)}
					<tr
						tabindex={k === tabKey ? 0 : -1}
						aria-selected={selected.has(k)}
						onfocus={() => (focusKey = k)}
						onkeydown={(e) => onrowkeydown(e, vm)}
						onclick={() => onselect(vm)}
						oncontextmenu={(e) => {
							if (!oncontextvm) return;
							e.preventDefault();
							oncontextvm(vm, e.clientX, e.clientY);
						}}
						class="cursor-pointer hover:bg-select-soft {activeKey === k
							? 'bg-select hover:bg-select'
							: selected.has(k)
								? 'bg-select-soft'
								: ''}"
					>
						<td class="px-3 py-1.5">
							<input
								type="checkbox"
								checked={selected.has(k)}
								onclick={(e) => e.stopPropagation()}
								onchange={() => toggleOne(vm)}
								aria-label="Select {vm.name}"
								class="cursor-pointer align-middle"
							/>
						</td>
						<td
							class="px-3 py-1.5 font-medium {sc?.kind === 'delete'
								? 'text-ink-faint line-through'
								: 'text-ink'}"
						>
							<!-- A real link: the keyboard (and middle-click) path into the
							     detail the row's mouse onclick can't provide. -->
							<a
								href={vmHref(vm.namespace, vm.name)}
								onclick={(e) => e.stopPropagation()}
								class="hover:underline focus-visible:underline">{vm.name}</a
							>
						</td>
						<td class="px-3 py-1.5 text-ink-muted">{vm.namespace}</td>
						<td class="px-3 py-1.5" title={SYNC_TITLE[vm.sync] ?? SYNC_TITLE.Unknown}>
							<span class="flex min-w-0 items-center gap-2">
								<span class="flex shrink-0 items-center gap-1.5 text-ink-soft">
									<span
										class="inline-block h-2 w-2 rounded-full {st.tone === 'neutral'
											? 'border border-ink-faint'
											: TONE_DOT[st.tone]}"
									></span>
									{st.word}
								</span>
								{#if st.exception}
									<StatusPill
										tone={st.exception.tone}
										label={st.exception.label}
										dot={false}
										title={st.exception.detail ?? st.exception.hint}
									/>
									{#if st.exception.detail}
										<span class="max-w-56 truncate font-mono text-[11px] text-ink-muted"
											>{st.exception.detail}</span
										>
									{/if}
								{/if}
							</span>
						</td>
						<td class="px-3 py-1.5 {vm.nodeName ? 'text-ink-soft' : 'text-ink-muted'}"
							>{vm.nodeName ?? '-'}</td
						>
						<td class="px-3 py-1.5 text-right text-ink-soft" title={vm.instancetype}
							>{size.cpu ?? '-'}</td
						>
						<td class="px-3 py-1.5 text-right text-ink-soft" title={vm.instancetype}
							>{size.memory ? quantity(size.memory) : '-'}</td
						>
						<td class="px-3 py-1.5 font-mono text-xs text-ink-soft">{vm.guestIP ?? '-'}</td>
						<td class="px-3 py-1.5">
							<span class="flex items-center gap-2">
								{#if sc}<StagedBadge item={sc} onopen={() => onstagedopen(vm)} />{/if}
								{#each prsOf.get(k) ?? [] as { pr, line } (pr.project + pr.prNumber)}
									<a
										href={pr.prURL}
										target="_blank"
										title={pr.title}
										onclick={(e) => e.stopPropagation()}
										class="text-xs text-ink-soft hover:underline"
										>PR #{pr.prNumber}{pr.mine ? '' : pr.by ? ` by ${pr.by}` : ''}{line
											? `: ${line}`
											: ''}</a
									>
								{/each}
							</span>
						</td>
						<td class="px-3 py-1.5">
							{#if next}
								<button
									type="button"
									onclick={(e) => {
										e.stopPropagation();
										next.run();
									}}
									class="text-xs text-accent-ink hover:underline">{next.label}</button
								>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>

		{#if rows.length === 0}
			<div class="p-8 text-center text-sm text-ink-faint">
				{vms.length === 0
					? 'No VMs in scope.'
					: prefs.value.filter === 'issues' && !search
						? 'No VM in scope has an issue.'
						: 'No VMs match the current filter.'}
			</div>
		{/if}
	</div>
</div>
