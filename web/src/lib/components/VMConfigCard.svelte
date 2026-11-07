<script lang="ts">
	import { Check, GitBranch, Pencil } from '@lucide/svelte';
	import {
		api,
		vmKey,
		type Change,
		type DraftItem,
		type HostCapacity,
		type Node,
		type VM,
	} from '#lib/api.js';
	import { bytes, friendlyError, quantity, quantityBytes } from '#lib/format.js';
	import { memoryFit } from '#lib/headroom.js';
	import {
		composeEdit,
		FIELD_LABEL,
		inlineBlock,
		type InlineField,
		type InlineValue,
	} from '#lib/inlineedit.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { vmSizing } from '#lib/sizing.js';
	import { TONE_PILL } from '#lib/status.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
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
	};
	const rows = $derived.by((): Row[] => [
		{
			key: 'power',
			label: 'Power',
			git: vm.power,
			fields: ['Power'],
			inline: { field: 'power', gitValue: vm.power },
			live: status,
		},
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
			key: 'eviction',
			label: 'Eviction',
			git: vm.evictionStrategy || 'Cluster default',
			fields: ['Eviction strategy'],
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

	const composed = (r: Row) =>
		composeEdit(
			stagedItem?.edit as Parameters<typeof composeEdit>[0],
			r.inline!.field,
			r.inline!.field === 'cpuCores' ? Number(value) : value,
			r.inline!.gitValue,
		);

	// Ask the backend what staging would change, a beat after typing stops.
	// Keyed on the edit itself, so a live frame never re-asks.
	const previewKey = $derived.by(() => {
		const r = rows.find((x) => x.key === editing);
		return r?.inline && !invalid(r) ? JSON.stringify(composed(r)) : '';
	});
	let seq = 0;
	$effect(() => {
		const k = previewKey;
		if (!k) {
			preview = { changes: null, error: '', loading: false };
			return;
		}
		const edit = JSON.parse(k) as ReturnType<typeof composed>;
		const mine = ++seq;
		preview = { changes: null, error: '', loading: true };
		const t = setTimeout(async () => {
			try {
				const changes = edit ? await api.previewEdit(vm.namespace, vm.name, edit) : [];
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
	const fitInputs = resource<{ cap: HostCapacity | null; nodes: Node[] | null } | null>(
		() => (editing === 'memory' ? 'fit' : ''),
		async (k) => {
			if (!k) return null;
			const [cap, nodes] = await Promise.all([
				api.capacity().catch(() => null),
				api.nodes().catch(() => null),
			]);
			return { cap, nodes };
		},
	);
	const fitHint = $derived.by((): { warn: boolean; text: string } | null => {
		const cap = fitInputs.data?.cap;
		if (editing !== 'memory' || !cap) return null;
		const need = quantityBytes(String(value));
		const current = quantityBytes(vm.memoryActual || sizing.memory || '') || 0;
		if (!(need > current)) return null;
		const f = memoryFit(cap, fitInputs.data?.nodes ?? null, need, {
			node: vm.nodeName,
			bytes: current,
		});
		if (!f?.best) return null;
		const most = `Most room: ${f.best.node}, ${bytes(f.best.free)} free.`;
		return f.fits
			? {
					warn: false,
					text: `Fits on ${f.fits} of ${f.hosts} host${f.hosts === 1 ? '' : 's'}. ${most}`,
				}
			: {
					warn: true,
					text: `No host has ${quantity(String(value))} free. ${most} ${vm.name} could not start until a host frees memory.`,
				};
	});

	const project = $derived(inventory.projectOf(vm.namespace));
	const stageOp = action();
	async function stage(r: Row, edit = composed(r)) {
		const label = r.label;
		const ok = await stageOp.run(async () => {
			if (edit) await api.stageEdit(vm.namespace, vm.name, { sourceFile: vm.sourceFile, ...edit });
			else await api.unstage(vm.namespace, vm.name, undefined, project);
		});
		if (!ok) return;
		editing = null;
		if (edit) ui.toastStaged(`${label} staged for ${vm.name}. It applies when the PR merges.`);
		else {
			drafts.refresh();
			ui.showToast(`Nothing left staged for ${vm.name}.`, { kind: 'success' });
		}
	}

	function unstageField(r: Row) {
		if (!r.inline) return;
		const edit = composeEdit(
			stagedItem?.edit as Parameters<typeof composeEdit>[0],
			r.inline.field,
			r.inline.gitValue ?? '',
			r.inline.gitValue ?? '',
		);
		void stage(r, edit);
	}
</script>

<InfoCard title="Configuration">
	{#snippet action()}
		{#if onedit && vm.sourceFile && canAuthor}
			<Button variant="link" size="sm" onclick={onedit}>Edit all settings</Button>
		{/if}
	{/snippet}
	<p class="border-b border-line-soft px-3 py-1.5 text-xs text-ink-muted">
		What git main declares, what runs, and what is staged. Edits stage; nothing reaches the cluster
		until its pull request merges.
	</p>
	<div class="overflow-x-auto">
		<table class="w-full text-[13px]">
			<thead>
				<tr class="border-b border-line text-left text-xs text-ink-muted">
					<th class="w-32 px-3 py-1.5 font-medium">Setting</th>
					<th class="w-1/2 px-3 py-1.5 font-medium">In git (main)</th>
					<th class="px-3 py-1.5 font-medium">Live on the cluster</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-line-soft">
				{#each rows as r (r.key)}
					{@const staged = stagedItem?.changes.filter((c) => owns(r, c)) ?? []}
					{@const drift = driftChanges?.filter((c) => owns(r, c)) ?? []}
					{@const block = r.inline
						? inlineBlock(vm, r.inline.field, stagedItem) ||
							(canAuthor ? '' : 'Read-only: you may not edit VMs here')
						: ''}
					<tr class="align-top {editing === r.key ? 'bg-select-soft' : ''}">
						<td class="px-3 py-2 text-ink-soft">{r.label}</td>
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
										<input
											aria-label={r.label}
											type={r.inline.field === 'cpuCores' ? 'number' : 'text'}
											min={r.inline.field === 'cpuCores' ? 1 : undefined}
											bind:value
											data-autofocus
											class="w-24 rounded border border-accent bg-panel px-2 py-1 text-[13px] text-ink"
										/>
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
									<p class="mt-1 text-xs text-danger-ink">{preview.error}</p>
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
								{#if stageOp.error}<p class="mt-1 text-xs text-danger-ink">{stageOp.error}</p>{/if}
							{:else if staged.length}
								<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
									{#each staged as c (c.field)}
										<span class="text-ink-muted line-through"
											>{c.action === 'add' ? '' : c.from}</span
										>
										<span class="font-medium text-accent-ink"
											>{c.action === 'remove' ? 'removed' : c.to}</span
										>
									{/each}
									<span
										class="inline-flex items-center gap-1 rounded-full px-1.5 text-xs {TONE_PILL.info}"
										><GitBranch size={11} />Staged</span
									>
									<span class="text-xs text-ink-muted"
										>{staged.some((c) => c.restart) ? 'restart on merge' : 'no restart'}</span
									>
									{#if r.inline && !block}
										<button class="text-xs text-accent-ink hover:underline" onclick={() => start(r)}
											>Change</button
										>
										<button
											class="text-xs text-accent-ink hover:underline"
											onclick={() => unstageField(r)}>Unstage</button
										>
									{/if}
								</div>
							{:else}
								<span class="flex items-center gap-2">
									<span class="text-ink">{r.git}</span>
									{#if r.inline}
										<button
											onclick={() => start(r)}
											disabled={!!block}
											title={block || `Change ${r.label.toLowerCase()}; it stages into a PR`}
											aria-label="Change {r.label}"
											class="rounded p-0.5 text-ink-muted hover:bg-inset hover:text-accent-ink disabled:opacity-40 disabled:hover:bg-transparent"
											><Pencil size={12} /></button
										>
									{/if}
								</span>
							{/if}
						</td>
						<td class="px-3 py-2">
							{#if drift.length}
								<span class="flex flex-wrap items-center gap-2">
									<span class="text-ink">{drift.map(show).join(', ')}</span>
									<span class="rounded-full px-1.5 text-xs {TONE_PILL.warn}">Live differs</span>
								</span>
							{:else if r.live}
								<span class="text-ink-soft">{r.live}</span>
							{:else if driftChanges === null}
								<span class="text-ink-muted" title="Live state not read yet">-</span>
							{:else}
								<span class="inline-flex items-center gap-1 text-xs text-ink-muted"
									><Check size={12} /> Same</span
								>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</InfoCard>
