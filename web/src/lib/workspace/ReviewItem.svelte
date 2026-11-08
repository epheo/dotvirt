<script lang="ts">
	import { Undo2 } from '@lucide/svelte';
	import {
		api,
		vmKey,
		type DraftItem,
		type DraftView,
		type ProposeResult,
		type VM,
	} from '#lib/api.js';
	import { bytes, plural, quantityBytes } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import ChangeList from '#lib/components/ChangeList.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import GitOpsStepper from '#lib/components/GitOpsStepper.svelte';
	import ManifestDiff from '#lib/components/ManifestDiff.svelte';
	import PruneAck from '#lib/components/PruneAck.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import TextArea from '#lib/components/TextArea.svelte';
	import TextInput from '#lib/components/TextInput.svelte';
	import ReviewPane from './ReviewPane.svelte';
	import ReviewSection from './ReviewSection.svelte';

	// A staged item under review: its field diff and what merging does to the
	// running system; below it, the propose card, the one primary action, which
	// opens one pull request for the whole project draft.
	let {
		project,
		item,
		lane,
		vm,
		onproposed,
	}: {
		project: string;
		item: DraftItem;
		// The project draft the item belongs to: its count and default PR text.
		lane?: DraftView;
		// The live object, for the impact section.
		vm: VM | null;
		onproposed: (r: ProposeResult & { title?: string }) => void;
	} = $props();

	const restart = $derived(item.changes.filter((c) => c.restart));
	const noun = $derived(!item.resource || item.resource === 'vm' ? 'VM' : 'object');
	const kindLabel = $derived(item.kind.charAt(0).toUpperCase() + item.kind.slice(1));

	// "+2 vCPU, +4.0 GiB memory" from the CPU/Memory change entries, when both
	// sides parse.
	const delta = $derived.by(() => {
		const parts: string[] = [];
		for (const c of item.changes) {
			if (c.action !== 'change' || !c.from || !c.to) continue;
			if (c.field === 'CPU') {
				const [f, t] = [c.from, c.to].map((s) => parseInt(s, 10));
				if (Number.isFinite(f) && Number.isFinite(t) && t !== f)
					parts.push(`${t > f ? '+' : ''}${t - f} vCPU`);
			} else if (c.field === 'Memory') {
				const d = quantityBytes(c.to) - quantityBytes(c.from);
				if (Number.isFinite(d) && d !== 0)
					parts.push(`${d > 0 ? '+' : '-'}${bytes(Math.abs(d))} memory`);
			}
		}
		return parts.join(', ');
	});

	const unstageOp = action();
	async function unstage() {
		if (unstageOp.busy) return;
		proposeOp.clear();
		if (await unstageOp.run(() => api.unstage(item.namespace, item.name, item.resource, project)))
			drafts.refresh();
	}

	// The propose form is per project; a selection move across projects must
	// not carry a half-typed title along. Keyed on the project name, not the
	// item: the item is rebuilt on every drafts refresh (a dock refresh, a
	// staging callback, another tab's merge) and would wipe a title mid-typing.
	let title = $state('');
	let message = $state('');
	// Merging this draft may let the sync delete live objects: the same
	// explicit yes the review sheet asks for, reset with the project.
	let ack = $state(false);
	const prune = $derived(lane?.prune ?? []);
	const blocked = $derived(prune.length > 0 && !ack);
	$effect(() => {
		project;
		title = '';
		message = '';
		ack = false;
	});
	const proposeOp = action();
	async function propose() {
		if (proposeOp.busy || blocked) return;
		unstageOp.clear();
		await proposeOp.run(async () => {
			const r = await api.propose(project, title, message);
			onproposed({ ...r, title: title || lane?.defaultTitle });
			title = '';
			message = '';
		});
		// Success or failure, re-read: the push may have landed before an error.
		drafts.refresh();
	}
	const count = $derived(lane?.count ?? 0);
	const others = $derived(count - 1);
</script>

<ReviewPane title={item.namespace ? vmKey(item) : item.name} mono>
	{#snippet sub()}{kindLabel} staged in {project}, not proposed yet.{/snippet}

	<ReviewSection title="Changes against git main">
		<ChangeList changes={item.changes} />
	</ReviewSection>

	<ReviewSection title="What merging does">
		<div class="flex flex-col gap-1.5">
			{#if item.kind === 'delete'}
				<Consequence tone="danger">
					Merging removes this {noun} from git, then ArgoCD deletes it from the cluster.
					{#if vm?.power === 'On'}<b class="font-medium text-ink"
							>It is running now{vm.nodeName ? ` on ${vm.nodeName}` : ''}.</b
						>{/if}
				</Consequence>
			{:else if item.kind === 'create'}
				<Consequence tone="info"
					>ArgoCD creates this object after the pull request merges.</Consequence
				>
			{:else if restart.length > 0}
				<Consequence tone="warn">
					<b class="font-medium text-ink">Restart required:</b>
					{restart.map((c) => c.field).join(', ')} appl{restart.length > 1 ? 'y' : 'ies'} at the next
					power cycle{#if vm?.power === 'On'}; the VM keeps running until you restart it{/if}.
				</Consequence>
			{:else}
				<Consequence tone="ok">
					Applies without a restart once the pull request merges and syncs.
				</Consequence>
			{/if}
			{#if delta && vm?.nodeName}
				<Consequence>
					Capacity: {delta} on <b class="font-mono font-medium text-ink">{vm.nodeName}</b> once applied.
				</Consequence>
			{/if}
		</div>
	</ReviewSection>

	{#if item.yaml}
		<details class="group px-card py-3">
			<summary class="cursor-pointer text-[13px] font-bold text-ink"
				>{item.baseYAML ? 'Manifest diff' : 'Manifest'}</summary
			>
			<div class="mt-2 overflow-hidden rounded-md border border-line-soft">
				<ManifestDiff before={item.baseYAML} after={item.yaml} />
			</div>
		</details>
	{/if}

	{#snippet footer()}
		<ErrorNote error={unstageOp.error} />
		<div class="flex flex-wrap items-center gap-2">
			<Button variant="danger-secondary" size="sm" onclick={unstage} disabled={unstageOp.busy}>
				<Undo2 size={14} />{unstageOp.busy ? 'Unstaging...' : 'Unstage'}
			</Button>
		</div>
		<p class="text-xs leading-4 text-ink-faint">
			Drops this change from the {project} draft{others === 1
				? '; the other one stays staged'
				: others > 1
					? `; the other ${others} stay staged`
					: ''}. Nothing reaches git.
		</p>
	{/snippet}
</ReviewPane>

<Card title="Propose {project}" meta={plural(count, 'staged change')}>
	<div class="flex flex-col gap-3">
		<GitOpsStepper stage="staged" />
		{#if prune.length}<div class="text-[13px]"><PruneAck {prune} bind:ack /></div>{/if}
		<TextInput
			bind:value={title}
			suggest={lane?.defaultTitle}
			placeholder="Pull request title"
			aria-label="Pull request title"
		/>
		<TextArea
			bind:value={message}
			suggest={lane?.defaultBody}
			placeholder="Description (optional; the change summary is appended)"
			aria-label="Pull request description"
		/>
		<p class="-mt-1.5 text-xs leading-4 text-ink-faint">
			Blank fields use the text shown; Tab takes it to edit.
		</p>
		<ErrorNote error={proposeOp.error} />
		<div class="flex flex-col gap-1.5">
			<div class="flex flex-wrap items-center gap-2">
				<Button onclick={propose} disabled={proposeOp.busy || blocked}>
					{proposeOp.busy ? 'Proposing...' : 'Propose pull request'}
				</Button>
				<StageChip kind="pr" />
			</div>
			{#if blocked}
				<p class="text-xs leading-4 text-danger-ink">Tick the deletion box to continue</p>
			{/if}
			<Consequence size="sm">
				Opens one pull request for the {plural(count, 'staged change')} in {project}. Nothing
				changes until it is approved and merged in the forge.
			</Consequence>
		</div>
	</div>
</Card>
