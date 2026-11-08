<script lang="ts">
	import { ArrowRight, Check, GitBranch, Minus, Pencil, Plus, Undo2 } from '@lucide/svelte';
	import { api, vmKey, type Change, type DraftItem, type VM } from '#lib/api.js';
	import { vmGate } from '#lib/gates.js';
	import { friendlyError, quantity, quantityBytes } from '#lib/format.js';
	import { memoryHint } from '#lib/headroom.js';
	import {
		FIELD_LABEL,
		fieldEdit,
		inlineBlock,
		type InlineField,
		type InlineValue,
	} from '#lib/inlineedit.js';
	import { action } from '#lib/resource.svelte.js';
	import { memoryRoom } from '#lib/state/hosts.svelte.js';
	import { vmSizing } from '#lib/sizing.js';
	import { TONE_PILL } from '#lib/status.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import InfoCard from './InfoCard.svelte';

	// Desired against actual, field by field: what git main declares, what runs,
	// and what is staged on its way. Power, vCPU and memory edit in place; the
	// consequence (restart, start, stop) is read from the backend before the
	// user stages. Everything else edits in Edit settings.
	let {
		vm,
		stagedItem = null,
		driftChanges,
		canAuthor,
		onedit,
	}: {
		vm: VM;
		stagedItem?: DraftItem | null;
		// null = live state not read (loading, or unreadable).
		driftChanges: Change[] | null;
		canAuthor: boolean;
		onedit?: () => void;
	} = $props();

	const sizing = $derived(vmSizing(vm, inventory.options));
	const status = $derived(vm.paused ? 'Paused' : (vm.phase ?? '-'));

	type Row = {
		key: string;
		label: string;
		git: string;
		// The change labels this row owns, in staged and drift lists alike.
		fields: string[];
		inline?: { field: InlineField; gitValue: InlineValue | undefined };
		live?: string;
		// A row with no live counterpart, and where to read more.
		gitOnly?: boolean;
		link?: { label: string; href: string };
	};
	const rows = $derived.by((): Row[] => [
		{
			key: 'cpu',
			label: 'vCPU',
			git: sizing.cpu ? String(sizing.cpu) : '-',
			fields: ['CPU'],
			inline: { field: 'cpuCores', gitValue: vm.cpuCores },
			live: vm.vcpus ? String(vm.vcpus) : undefined,
		},
		{
			key: 'memory',
			label: 'Memory',
			git: sizing.memory ? quantity(sizing.memory) : '-',
			fields: ['Memory'],
			inline: { field: 'memory', gitValue: vm.memory },
			live: vm.memoryActual ? quantity(vm.memoryActual) : undefined,
		},
		{
			key: 'instancetype',
			label: 'Instance type',
			git: vm.instancetype || 'None, custom size',
			fields: ['Instance type'],
		},
		{
			key: 'preference',
			label: 'Preference',
			git: vm.preference || 'None',
			fields: ['Preference'],
		},
		{
			key: 'labels',
			label: 'Labels',
			git: Object.keys(vm.labels ?? {}).length
				? Object.entries(vm.labels ?? {})
						.map(([k, v]) => `${k}=${v}`)
						.join(', ')
				: 'None',
			fields: ['Label '],
		},
		{
			key: 'disks',
			label: 'Disks',
			git: vm.disks?.length
				? vm.disks
						.map((d) =>
							[d.name, d.size && quantity(d.size), d.storageClass].filter(Boolean).join(' '),
						)
						.join(', ')
				: 'None',
			fields: ['Disk'],
		},
		{
			key: 'networks',
			label: 'Networks',
			git: vm.networks?.length ? vm.networks.map((n) => n.network || n.name).join(', ') : 'None',
			fields: ['Network'],
			live: vm.guestIP
				? `${vm.networks?.map((n) => n.network || n.name).join(', ') || 'pod'}, ${vm.guestIP}`
				: undefined,
		},
		{
			key: 'power',
			label: 'Power',
			git: vm.power,
			fields: ['Power'],
			inline: { field: 'power', gitValue: vm.power },
			live: status,
		},
		{
			key: 'eviction',
			label: 'Eviction',
			git: vm.evictionStrategy || 'Cluster default',
			fields: ['Eviction strategy'],
		},
		{
			key: 'source',
			label: 'Source file',
			git: vm.sourceFile || 'Not in git',
			fields: [],
			gitOnly: true,
			link: vm.sourceFile ? { label: 'History', href: '?tab=changes' } : undefined,
		},
	]);

	const owns = (r: Row, c: Change) => r.fields.some((f) => c.field === f || c.field.startsWith(f));
	const show = (c: Change) =>
		c.action === 'remove' ? `${c.from} removed` : c.action === 'add' ? `${c.to} added` : c.to;

	// One row edits at a time; the draft value and its preview live here.
	let editing = $state<string | null>(null);
	let value = $state<InlineValue>('');
	let preview = $state<{ changes: Change[] | null; error: string; loading: boolean }>({
		changes: null,
		error: '',
		loading: false,
	});
	// Keyed on identity: the live stream hands down a fresh vm every frame.
	const key = $derived(vmKey(vm));
	$effect(() => {
		key;
		editing = null;
	});

	function start(r: Row) {
		if (!r.inline) return;
		editing = r.key;
		value =
			r.inline.field === 'power'
				? (stagedItem?.edit?.power ?? vm.power)
				: r.inline.field === 'cpuCores'
					? (stagedItem?.edit?.cpuCores ?? sizing.cpu ?? 1)
					: (stagedItem?.edit?.memory ?? vm.memory ?? '');
	}

	const invalid = (r: Row) =>
		r.inline?.field === 'memory' && !(quantityBytes(String(value)) > 0)
			? 'Use a size like 8Gi'
			: r.inline?.field === 'cpuCores' && !(Number(value) >= 1 && Number.isInteger(Number(value)))
				? 'Use a whole number of vCPU'
				: '';

	const edit = (r: Row) => fieldEdit(r.inline!.field, value);

	// Ask the backend what staging would change, a beat after typing stops.
	// Keyed on the edit itself, so a live frame never re-asks.
	const previewKey = $derived.by(() => {
		const r = rows.find((x) => x.key === editing);
		return r?.inline && !invalid(r) ? JSON.stringify(edit(r)) : '';
	});
	let seq = 0;
	$effect(() => {
		const k = previewKey;
		if (!k) {
			preview = { changes: null, error: '', loading: false };
			return;
		}
		const next = JSON.parse(k) as ReturnType<typeof edit>;
		const mine = ++seq;
		preview = { changes: null, error: '', loading: true };
		const t = setTimeout(async () => {
			try {
				const changes = await api.previewEdit(vm.namespace, vm.name, next);
				if (mine === seq) preview = { changes, error: '', loading: false };
			} catch (e) {
				if (mine === seq) preview = { changes: null, error: friendlyError(e), loading: false };
			}
		}, 300);
		return () => clearTimeout(t);
	});

	function consequence(r: Row): { tone: 'warn' | 'ok'; text: string } | null {
		if (!r.inline || !preview.changes) return null;
		const c = preview.changes.find((x) => x.field === FIELD_LABEL[r.inline!.field]);
		if (!c) return { tone: 'ok', text: 'Same as git main: staging this unstages the field.' };
		if (r.inline.field === 'power')
			return {
				tone: 'warn',
				text: `${vm.name} ${c.to === 'Off' ? 'stops' : 'starts'} when the PR merges.`,
			};
		return c.restart
			? { tone: 'warn', text: `${vm.name} restarts once when the PR merges.` }
			: { tone: 'ok', text: 'Applies without a restart when the PR merges.' };
	}

	// Memory only: guest memory is reserved per VM, so an increase past every
	// host's free memory leaves the VM unable to start; vCPU is time-shared.
	// Capacity is metrics-backed: with metrics off the hint stays silent.
	const room = memoryRoom(() => editing === 'memory');
	const fitHint = $derived(
		editing === 'memory' ? memoryHint(room.data, vm, sizing.memory, String(value)) : null,
	);

	const stageOp = action();
	async function stage(r: Row, next = edit(r)) {
		const label = r.label;
		let left = true;
		const ok = await stageOp.run(async () => {
			const view = await api.stageEdit(vm.namespace, vm.name, {
				sourceFile: vm.sourceFile,
				...next,
			});
			left = view.items.some(
				(i) =>
					(!i.resource || i.resource === 'vm') &&
					i.namespace === vm.namespace &&
					i.name === vm.name,
			);
		});
		if (!ok) return;
		editing = null;
		if (left) ui.toastStaged(`${label} staged for ${vm.name}. It applies when the PR merges.`);
		else {
			drafts.refresh();
			ui.showToast(`Nothing left staged for ${vm.name}.`, { kind: 'success' });
		}
	}

	function unstageField(r: Row) {
		if (r.inline) void stage(r, fieldEdit(r.inline.field, r.inline.gitValue));
	}
</script>

<InfoCard title="Configuration">
	{#snippet action()}
		<span class="flex min-w-0 items-center gap-3">
			<span class="ml-4 hidden truncate text-xs text-ink-muted md:inline"
				>{canAuthor && vm.sourceFile ? 'Click a value in git to change it. ' : ''}Nothing reaches
				the cluster until its PR merges.</span
			>
			{#if onedit && vm.sourceFile && canAuthor}
				<Button variant="link" size="sm" class="shrink-0" onclick={onedit}>Edit all settings</Button
				>
			{/if}
		</span>
	{/snippet}
	<div class="overflow-x-auto">
		<table class="w-full text-[13px]">
			<thead>
				<tr class="border-b border-line text-left text-xs text-ink-muted">
					<th class="w-32 py-1.5 pr-3 pl-card font-medium">Setting</th>
					<th class="w-2/5 px-3 py-1.5 font-medium">In git (main)</th>
					<th class="px-3 py-1.5 font-medium">Live on the cluster</th>
					<th class="w-px py-1.5 pr-card pl-3"><span class="sr-only">Actions</span></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-line-soft">
				{#each rows as r (r.key)}
					{@const staged = stagedItem?.changes.filter((c) => owns(r, c)) ?? []}
					{@const drift = driftChanges?.filter((c) => owns(r, c)) ?? []}
					{@const block = r.inline
						? inlineBlock(vm, r.inline.field, stagedItem) || vmGate.update(vm.namespace).why
						: ''}
					<tr class="align-top {editing === r.key ? 'bg-select-soft' : ''}">
						<td class="py-2 pr-3 pl-card text-ink-soft">{r.label}</td>
						<td class="px-3 py-2">
							{#if editing === r.key && r.inline}
								{@const err = invalid(r)}
								{@const cq = consequence(r)}
								<form
									class="flex flex-wrap items-center gap-2"
									onsubmit={(e) => {
										e.preventDefault();
										if (!err) void stage(r);
									}}
								>
									{#if r.inline.field === 'power'}
										<span
											class="inline-flex overflow-hidden rounded border border-line-strong"
											role="group"
											aria-label="Power"
										>
											{#each ['On', 'Off'] as p (p)}
												<button
													type="button"
													aria-pressed={value === p}
													onclick={() => (value = p)}
													class="px-3 py-1 text-xs font-medium {value === p
														? 'bg-accent text-white'
														: 'bg-panel text-ink-soft hover:bg-inset'}">{p}</button
												>
											{/each}
										</span>
									{:else}
										{#if r.inline.field === 'cpuCores'}
											<span class="inline-flex items-center">
												<button
													type="button"
													aria-label="One vCPU fewer"
													disabled={Number(value) <= 1}
													onclick={() => (value = Math.max(1, Number(value) - 1))}
													class="rounded-l border border-r-0 border-line-strong bg-panel px-2 py-1.5 text-ink-soft hover:bg-inset disabled:opacity-40"
													><Minus size={12} /></button
												>
												<input
													aria-label={r.label}
													type="number"
													min="1"
													bind:value
													data-autofocus
													class="w-14 border border-accent bg-panel px-2 py-1 text-center text-[13px] text-ink"
												/>
												<button
													type="button"
													aria-label="One vCPU more"
													onclick={() => (value = Number(value) + 1)}
													class="rounded-r border border-l-0 border-line-strong bg-panel px-2 py-1.5 text-ink-soft hover:bg-inset"
													><Plus size={12} /></button
												>
											</span>
										{:else}
											<input
												aria-label={r.label}
												type="text"
												bind:value
												data-autofocus
												class="w-24 rounded border border-accent bg-panel px-2 py-1 text-[13px] text-ink"
											/>
										{/if}
									{/if}
									<Button
										size="sm"
										type="submit"
										disabled={!!err || stageOp.busy || preview.loading}>Stage</Button
									>
									<Button size="sm" variant="ghost" type="button" onclick={() => (editing = null)}
										>Cancel</Button
									>
								</form>
								{#if err}
									<p class="mt-1 text-xs text-danger-ink">{err}</p>
								{:else if preview.error}
									<ErrorNote error={preview.error} class="mt-1" />
								{:else if cq}
									<p class="mt-1 text-xs {cq.tone === 'warn' ? 'text-warn-ink' : 'text-ok-ink'}">
										{cq.text}
									</p>
								{/if}
								{#if fitHint && !err}
									<p class="mt-1 text-xs {fitHint.warn ? 'text-warn-ink' : 'text-ink-muted'}">
										{fitHint.text}
									</p>
								{/if}
								<ErrorNote error={stageOp.error} class="mt-1" />
							{:else if staged.length}
								<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
									{#each staged as c (c.field)}
										{#if c.action !== 'add'}<span class="text-ink-muted line-through">{c.from}</span
											><ArrowRight size={12} class="text-ink-faint" />{/if}
										{#if r.inline && !block}
											<button
												onclick={() => start(r)}
												title={`Change ${r.label.toLowerCase()}; it stages into a PR`}
												class="-mx-1 rounded px-1 font-semibold text-accent-ink hover:bg-inset"
												><span class="sr-only">Change {r.label}:</span>{c.to}</button
											>
										{:else}
											<span class="font-semibold text-accent-ink"
												>{c.action === 'remove' ? 'removed' : c.to}</span
											>
										{/if}
									{/each}
									<span
										class="inline-flex items-center gap-1 rounded-full px-1.5 text-xs {TONE_PILL.info}"
										><GitBranch size={11} />Staged</span
									>
									<span class="text-xs text-ink-muted"
										>{staged.some((c) => c.restart)
											? 'restarts when it merges'
											: 'applies without restart'}</span
									>
								</div>
							{:else}
								{#if r.inline && !block}
									<!-- The value is the control: click it to change it. -->
									<button
										onclick={() => start(r)}
										title={`Change ${r.label.toLowerCase()}; it stages into a PR`}
										class="group -mx-1 flex items-center gap-2 rounded px-1 text-ink hover:bg-inset"
										><span class="sr-only">Change {r.label}:</span>{r.git}<Pencil
											size={12}
											class="text-ink-muted group-hover:text-accent-ink"
										/></button
									>
								{:else}
									<span class="flex items-center gap-2">
										<span class={r.gitOnly ? 'font-mono text-xs text-ink-soft' : 'text-ink'}
											>{r.git}</span
										>
										{#if r.inline}
											<span title={block}
												><Pencil size={12} class="text-ink-muted opacity-40" /></span
											>
										{/if}
										{#if r.link}
											<a class="text-xs text-accent-ink hover:underline" href={r.link.href}
												>{r.link.label}</a
											>
										{/if}
									</span>
								{/if}
							{/if}
						</td>
						<td class="px-3 py-2">
							{#if r.gitOnly}
								<span class="text-ink-muted">-</span>
							{:else if drift.length}
								<span class="flex flex-wrap items-center gap-2">
									<span class="text-ink">{drift.map(show).join(', ')}</span>
									<span class="rounded-full px-1.5 text-xs {TONE_PILL.warn}">Live differs</span>
								</span>
							{:else if r.live}
								<span class="text-ink">{r.live}</span>
							{:else if driftChanges === null}
								<span class="text-ink-muted" title="Live state not read yet">-</span>
							{:else}
								<!-- Read and matching: the value it runs with, the match secondary. -->
								<span class="text-ink-soft"
									>{r.git}<span
										class="ml-2 inline-flex items-center gap-1 text-xs whitespace-nowrap text-ink-muted"
										><Check size={12} /> Same</span
									></span
								>
							{/if}
						</td>
						<td class="py-2 pr-card pl-3 text-right whitespace-nowrap">
							{#if staged.length && r.inline && !block}
								<button
									class="inline-flex items-center gap-1 text-xs text-accent-ink hover:underline"
									onclick={() => unstageField(r)}><Undo2 size={12} />Unstage</button
								>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
	<!-- An Unstage runs with no editor open: its refusal shows here. -->
	{#if !editing}<ErrorNote error={stageOp.error} class="m-3" />{/if}
</InfoCard>
