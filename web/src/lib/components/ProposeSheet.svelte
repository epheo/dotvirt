<script lang="ts">
	import { Check, Power, RotateCw, Server, Trash2, TriangleAlert } from '@lucide/svelte';
	import { api, type DraftItem } from '#lib/api.js';
	import { memoryHint } from '#lib/headroom.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { memoryRoom } from '#lib/state/hosts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { draftKindTone, TONE_PILL } from '#lib/status.js';
	import { changeSummary } from '#lib/tray.js';
	import Button from './Button.svelte';
	import ChangeList from './ChangeList.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import TextArea from './TextArea.svelte';
	import TextInput from './TextInput.svelte';

	// Review before the pull request: what changes, what merging does to what
	// runs, and what it deletes, which needs an explicit yes. Approval and
	// merge stay in the forge; the tray follows the PR from here.
	let { project, onclose }: { project: string; onclose: () => void } = $props();

	const lane = $derived(drafts.drafts.find((d) => d.project === project)?.draft);
	const prune = $derived(lane?.prune ?? []);

	// Prefilled once, never reset by a drafts refresh mid-typing.
	let title = $state('');
	let message = $state('');
	let editingText = $state(false);
	let filled = false;
	$effect(() => {
		if (!filled && lane) {
			title = lane.defaultTitle ?? '';
			filled = true;
		}
	});
	let ack = $state(false);

	const isVM = (it: DraftItem) => !it.resource || it.resource === 'vm';
	const kindName = (it: DraftItem) =>
		isVM(it) ? 'VirtualMachine' : (it.resource ?? '').replace(/^./, (x) => x.toUpperCase());
	const blocked = $derived(prune.length > 0 && !ack);
	const word = (f: string) => (f === f.toUpperCase() ? f : f.toLowerCase());
	const list = (xs: string[]) =>
		xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`;

	// One line on what is proposed, the way the PR will read.
	const scale = $derived.by(() => {
		const items = lane?.items ?? [];
		const n = items.reduce((t, it) => t + (it.kind === 'edit' ? it.changes.length : 1), 0);
		const noun = items.every(isVM) ? 'VM' : 'object';
		return `${n} change${n === 1 ? '' : 's'} to ${items.length} ${noun}${items.length === 1 ? '' : 's'}. Opens one pull request on ${lane?.base ?? 'main'}.`;
	});

	// What merging does to each object, worst first: deletes, restarts, power
	// moves, then everything that applies in place. A restart names the field
	// that causes it, so a mixed edit says which part could have gone live.
	// lead is the bold opening; icon names the kind of consequence.
	type Effect = {
		tone: 'danger' | 'warn' | 'ok';
		icon: 'delete' | 'restart' | 'power' | 'warn' | 'ok';
		lead?: string;
		text: string;
	};
	function consequence(it: DraftItem): Effect {
		if (it.kind === 'delete')
			return { tone: 'danger', icon: 'delete', lead: `${it.name} is deleted.`, text: '' };
		if (it.kind === 'create') return { tone: 'ok', icon: 'ok', text: `${it.name} is created.` };
		const power = it.changes.find((c) => c.field === 'Power');
		const restart = it.changes.filter((c) => c.restart).map((c) => word(c.field));
		const quiet = it.changes.filter((c) => !c.restart && c !== power).map((c) => word(c.field));
		if (restart.length)
			return {
				tone: 'warn',
				icon: 'restart',
				lead: `${it.name} restarts once.`,
				text:
					`The ${list(restart)} change${restart.length === 1 ? ' needs' : 's need'} it.` +
					(quiet.length
						? ` The ${list(quiet)} change${quiet.length === 1 ? '' : 's'} alone would not.`
						: ''),
			};
		if (power)
			return {
				tone: 'warn',
				icon: 'power',
				lead: `${it.name} ${power.to === 'Off' ? 'stops' : 'starts'}.`,
				text: '',
			};
		return { tone: 'ok', icon: 'ok', text: `${it.name} changes without a restart.` };
	}

	// A memory increase is checked against host room, the one hard limit a
	// restart can hit.
	const memoryUps = $derived(
		(lane?.items ?? []).flatMap((it) => {
			const c = it.kind === 'edit' && isVM(it) && it.changes.find((x) => x.field === 'Memory');
			return c && c.to ? [{ it, c }] : [];
		}),
	);
	const room = memoryRoom(() => memoryUps.length > 0);
	const fits = $derived(
		memoryUps.flatMap(({ it, c }): Effect[] => {
			const vm = inventory.findVM(it.namespace, it.name) ?? { name: it.name };
			const h = memoryHint(room.data, vm, c.from, c.to ?? '', true);
			return h
				? [{ tone: h.warn ? 'warn' : 'ok', icon: h.warn ? 'warn' : 'ok', text: h.text }]
				: [];
		}),
	);

	const RANK = { danger: 0, warn: 1, ok: 2 };
	const effects = $derived<Effect[]>([
		...[...(lane?.items ?? []).map(consequence), ...fits].sort(
			(a, b) => RANK[a.tone] - RANK[b.tone],
		),
		...(lane
			? [{ tone: 'ok' as const, icon: 'ok' as const, text: `Nothing else in ${project} changes.` }]
			: []),
	]);

	const unstageOp = action();
	async function unstage(it: DraftItem) {
		if (await unstageOp.run(() => api.unstage(it.namespace, it.name, it.resource, project)))
			drafts.refresh();
	}

	const op = action();
	async function propose() {
		if (op.busy || (prune.length && !ack)) return;
		// Read before the refresh empties the lane.
		const declared = changeSummary(lane?.items ?? []);
		const prTitle = title.trim() || lane?.defaultTitle || '';
		const ok = await op.run(async () => {
			const r = await api.propose(project, title.trim(), editingText ? message : '');
			if (r.prNumber)
				tray.remember({
					project,
					prNumber: r.prNumber,
					title: prTitle,
					prURL: r.prURL ?? '',
					...declared,
				});
			drafts.refresh();
			ui.toastProposed(project, r);
		});
		if (ok) onclose();
	}
</script>

<Modal title="Propose to {project}" subtitle={lane ? scale : undefined} size="3xl" drawer {onclose}>
	<div class="min-h-0 flex-1 space-y-6 overflow-y-auto px-6 py-5 text-sm">
		<ol class="grid grid-cols-4 gap-2 text-xs" aria-label="Where this change is">
			{#each ['Staged', 'Pull request', 'Approved and merged in the forge', 'Live on the cluster'] as step, i (step)}
				<li class="flex flex-col gap-1.5 {i === 0 ? 'font-semibold text-ink' : 'text-ink-muted'}">
					<span class="h-1 rounded-full {i === 0 ? 'bg-accent' : 'bg-line'}"></span>
					{step}
				</li>
			{/each}
		</ol>

		{#if !lane}
			<p class="text-ink-muted">Nothing staged in {project}.</p>
		{:else}
			<label class="block space-y-1.5">
				<span class="text-xs font-semibold tracking-wide text-ink-muted uppercase"
					>Pull request title</span
				>
				<TextInput bind:value={title} suggest={lane.defaultTitle} data-autofocus />
				<span class="block text-xs text-ink-muted">Written from the changes. Edit freely.</span>
			</label>

			<section class="space-y-2">
				<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">What changes</h3>
				{#each lane.items as it (it.kind + it.namespace + it.name)}
					<div class="overflow-hidden rounded border border-line">
						<div class="flex items-center gap-2 border-b border-line bg-inset px-3 py-2">
							<Server size={14} class="shrink-0 text-ink-muted" />
							<strong class="text-ink">{it.name}</strong>
							<span class="truncate text-ink-muted">{it.namespace}, {kindName(it)}</span>
							{#if it.kind !== 'edit'}
								<span class="rounded-full px-1.5 text-xs {TONE_PILL[draftKindTone(it.kind)]}"
									>{it.kind}</span
								>
							{/if}
							<Button
								class="ml-auto shrink-0"
								variant="link"
								size="sm"
								disabled={unstageOp.busy}
								onclick={() => unstage(it)}>Unstage all</Button
							>
						</div>
						<div class="px-3 py-2">
							<ChangeList changes={it.changes} pills={it.kind === 'edit' && isVM(it)} />
						</div>
					</div>
				{/each}
				<ErrorNote error={unstageOp.error} />
			</section>

			<section class="space-y-2">
				<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">When it merges</h3>
				<ul class="divide-y divide-line-soft rounded border border-line">
					{#each effects as e, i (i)}
						<li class="flex items-start gap-2.5 px-3 py-2">
							{#if e.icon === 'restart'}<RotateCw size={14} class="mt-0.5 shrink-0 text-warn-ink" />
							{:else if e.icon === 'power'}<Power size={14} class="mt-0.5 shrink-0 text-warn-ink" />
							{:else if e.icon === 'delete'}<Trash2
									size={14}
									class="mt-0.5 shrink-0 text-danger-ink"
								/>
							{:else if e.icon === 'warn'}<TriangleAlert
									size={14}
									class="mt-0.5 shrink-0 text-warn-ink"
								/>
							{:else}<Check size={14} class="mt-0.5 shrink-0 text-ok-ink" />{/if}
							<span class="text-ink-soft"
								>{#if e.lead}<strong class="text-ink">{`${e.lead} `}</strong>{/if}{e.text}</span
							>
						</li>
					{/each}
				</ul>
			</section>

			{#if prune.length}
				<section class="space-y-2">
					<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">
						Also in this merge
					</h3>
					<div class="space-y-2 rounded border border-danger/40 bg-danger-soft px-3 py-2.5">
						<p class="flex items-start gap-2 text-danger-ink">
							<TriangleAlert size={14} class="mt-0.5 shrink-0" />
							<span
								><strong
									>The sync deletes {prune.length} object{prune.length === 1 ? '' : 's'} from the cluster.</strong
								>
								Git main no longer declares {prune.length === 1 ? 'it' : 'them'}.</span
							>
						</p>
						<ul class="space-y-0.5 pl-6 font-mono text-xs text-ink">
							{#each prune as r (r.kind + r.namespace + r.name)}
								<li>
									{r.namespace ? `${r.namespace}/` : ''}{r.name}
									<span class="text-ink-muted">{r.kind}</span>
								</li>
							{/each}
						</ul>
						<div class="flex flex-wrap items-center gap-x-4 gap-y-1 pl-6">
							<label class="flex items-center gap-2 font-medium text-ink">
								<input type="checkbox" bind:checked={ack} />
								Delete {prune.length === 1 ? 'it' : `these ${prune.length}`} when this merges
							</label>
							<span class="text-xs text-ink-soft"
								>Not meant to go? Adopt {prune.length === 1 ? 'it' : 'them'} into git first, from Issues.</span
							>
						</div>
					</div>
				</section>
			{:else if lane.warning}
				<p class="rounded border border-warn/50 bg-warn-soft px-3 py-2 text-warn-ink">
					{lane.warning}
				</p>
			{/if}

			<section class="space-y-2">
				<div class="flex items-center justify-between">
					<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Description</h3>
					{#if !editingText}
						<Button
							variant="link"
							size="sm"
							onclick={() => {
								message = lane.defaultBody ?? '';
								editingText = true;
							}}>Edit text</Button
						>
					{/if}
				</div>
				{#if editingText}
					<TextArea bind:value={message} rows={8} mono aria-label="Pull request description" />
				{:else}
					<pre
						class="rounded border border-line bg-inset px-3 py-2 font-sans text-xs whitespace-pre-wrap text-ink-soft">{lane.defaultBody}</pre>
				{/if}
			</section>

			<ErrorNote error={op.error} />
		{/if}
	</div>

	{#snippet footer()}
		<span class="mr-auto max-w-72 text-xs text-ink-muted"
			>Approval and merge happen in the forge. The tray follows it until it is live.</span
		>
		<Button variant="ghost" onclick={onclose}>Keep editing</Button>
		<div class="flex flex-col items-end gap-1">
			<Button onclick={propose} disabled={!lane || op.busy || blocked}>
				{op.busy ? 'Opening...' : 'Open pull request'}
			</Button>
			{#if blocked}<span class="text-xs text-danger-ink">Tick the deletion box to continue</span
				>{/if}
		</div>
	{/snippet}
</Modal>
