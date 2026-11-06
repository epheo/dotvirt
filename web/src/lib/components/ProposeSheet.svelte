<script lang="ts">
	import { api, type DraftItem } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { tray } from '#lib/state/tray.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { draftKindTone, TONE_PILL } from '#lib/status.js';
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

	// What merging does to each object, worst first: deletes, restarts, power
	// moves, then everything that applies in place.
	function consequence(it: DraftItem): { tone: 'danger' | 'warn' | 'ok'; text: string } {
		if (it.kind === 'delete') return { tone: 'danger', text: `${it.name} is deleted` };
		if (it.kind === 'create') return { tone: 'ok', text: `${it.name} is created` };
		const power = it.changes.find((c) => c.field === 'Power');
		const restart = it.changes.filter((c) => c.restart).map((c) => c.field);
		if (restart.length)
			return { tone: 'warn', text: `${it.name} restarts once (${restart.join(', ')})` };
		if (power)
			return { tone: 'warn', text: `${it.name} ${power.to === 'Off' ? 'stops' : 'starts'}` };
		return { tone: 'ok', text: `${it.name} changes without a restart` };
	}
	const RANK = { danger: 0, warn: 1, ok: 2 };
	const effects = $derived(
		(lane?.items ?? []).map(consequence).sort((a, b) => RANK[a.tone] - RANK[b.tone]),
	);

	const op = action();
	async function propose() {
		if (op.busy || (prune.length && !ack)) return;
		const ok = await op.run(async () => {
			const r = await api.propose(project, title.trim(), editingText ? message : '');
			drafts.refresh();
			tray.open = true;
			ui.showToast(
				r.prNumber
					? `PR #${r.prNumber} ${r.existing ? 'updated' : 'opened'} in ${project}. Approve and merge it in the forge.`
					: `Pushed ${r.branch}. Open the pull request in the forge.`,
				{
					kind: 'success',
					action:
						r.prURL || r.compareURL
							? {
									label: r.prURL ? `Open PR #${r.prNumber}` : 'Open in forge',
									run: () => window.open(r.prURL || r.compareURL, '_blank', 'noopener'),
								}
							: undefined,
				},
			);
		});
		if (ok) onclose();
	}
</script>

<Modal title="Propose to {project}" size="3xl" {onclose}>
	<div class="max-h-[70vh] space-y-5 overflow-y-auto px-5 py-4 text-sm">
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
			</label>

			<section class="space-y-2">
				<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">What changes</h3>
				{#each lane.items as it (it.kind + it.namespace + it.name)}
					<div class="rounded border border-line">
						<div class="flex items-center gap-2 border-b border-line-soft bg-inset px-3 py-1.5">
							<strong class="text-ink">{it.namespace}/{it.name}</strong>
							<span class="rounded-full px-1.5 text-xs {TONE_PILL[draftKindTone(it.kind)]}"
								>{it.kind}</span
							>
						</div>
						<div class="px-3 py-2"><ChangeList changes={it.changes} /></div>
					</div>
				{/each}
			</section>

			<section class="space-y-2">
				<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">When it merges</h3>
				<ul class="divide-y divide-line-soft rounded border border-line">
					{#each effects as e, i (i)}
						<li class="flex items-center gap-2 px-3 py-1.5">
							<span class="rounded-full px-1.5 text-xs {TONE_PILL[e.tone]}"
								>{e.tone === 'ok' ? 'Fine' : e.tone === 'warn' ? 'Heads up' : 'Removes'}</span
							>
							{e.text}
						</li>
					{/each}
				</ul>
			</section>

			{#if prune.length}
				<section class="space-y-2 rounded border border-danger/30 bg-danger-soft px-3 py-2.5">
					<p class="font-medium text-danger-ink">
						The sync also deletes {prune.length} object{prune.length === 1 ? '' : 's'} from the cluster.
						Git main no longer declares {prune.length === 1 ? 'it' : 'them'}.
					</p>
					<ul class="space-y-0.5 font-mono text-xs text-ink">
						{#each prune as r (r.kind + r.namespace + r.name)}
							<li>
								{r.namespace ? `${r.namespace}/` : ''}{r.name}
								<span class="text-ink-muted">{r.kind}</span>
							</li>
						{/each}
					</ul>
					<label class="flex items-center gap-2 font-medium text-ink">
						<input type="checkbox" bind:checked={ack} />
						Delete {prune.length === 1 ? 'it' : `these ${prune.length}`} when this merges
					</label>
					<p class="text-xs text-ink-soft">
						Not meant to go? Adopt {prune.length === 1 ? 'it' : 'them'} into git first, from Issues.
					</p>
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
		<span class="mr-auto text-xs text-ink-muted"
			>Approval and merge happen in the forge. The tray follows it until it is live.</span
		>
		<Button variant="ghost" onclick={onclose}>Keep editing</Button>
		<Button onclick={propose} disabled={!lane || op.busy || (prune.length > 0 && !ack)}>
			{op.busy ? 'Opening...' : 'Open pull request'}
		</Button>
	{/snippet}
</Modal>
