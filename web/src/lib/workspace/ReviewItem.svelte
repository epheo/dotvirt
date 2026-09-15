<script lang="ts">
	import { Server, TriangleAlert } from 'lucide-svelte';
	import {
		api,
		vmKey,
		type DraftItem,
		type DraftView,
		type ProposeResult,
		type VM,
	} from '$lib/api';
	import { bytes, quantityBytes } from '$lib/format';
	import { action } from '$lib/resource.svelte';
	import { draftKindTone, TONE_PILL } from '$lib/status';
	import { drafts } from '$lib/state/drafts.svelte';
	import Button from '$lib/components/Button.svelte';
	import ChangeList from '$lib/components/ChangeList.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import GitOpsStepper from '$lib/components/GitOpsStepper.svelte';
	import ManifestDiff from '$lib/components/ManifestDiff.svelte';
	import TextArea from '$lib/components/TextArea.svelte';
	import TextInput from '$lib/components/TextInput.svelte';
	import ReviewPane from './ReviewPane.svelte';

	// A staged item under review: its field diff, what merging does to the
	// running system, and the propose footer - the one primary action, which
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
	$effect(() => {
		project;
		title = '';
		message = '';
	});
	const proposeOp = action();
	async function propose() {
		if (proposeOp.busy) return;
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
</script>

<ReviewPane title={item.namespace ? vmKey(item) : item.name}>
	{#snippet icon()}<Server size={16} class="text-ink-muted" />{/snippet}
	{#snippet head()}
		<span class="rounded px-1.5 py-0.5 text-xs {TONE_PILL[draftKindTone(item.kind)]}"
			>{item.kind}</span
		>
		<button
			onclick={unstage}
			disabled={unstageOp.busy}
			class="ml-auto text-xs text-danger hover:text-danger-ink disabled:text-ink-faint"
			>{unstageOp.busy ? 'unstaging…' : 'unstage'}</button
		>
	{/snippet}

	<section class="rounded border border-line">
		<div class="border-b border-line bg-inset px-3 py-1.5">
			<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">
				Changes against git main
			</h3>
		</div>
		<div class="px-3 py-2">
			<ChangeList changes={item.changes} />
		</div>
	</section>

	<section class="rounded border border-line">
		<div class="border-b border-line bg-inset px-3 py-1.5">
			<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Impact</h3>
		</div>
		<div class="space-y-2 px-3 py-2 text-xs text-ink-soft">
			{#if item.kind === 'delete'}
				<p class="flex items-start gap-2">
					<TriangleAlert size={14} class="mt-0.5 shrink-0 text-danger-ink" />
					<span>
						Merging removes this VM from git; ArgoCD then deletes it from the cluster
						{#if vm?.power === 'On'}<b class="font-medium">
								— it is running now{vm.nodeName ? ` on ${vm.nodeName}` : ''}</b
							>{/if}.
					</span>
				</p>
			{:else if item.kind === 'create'}
				<p>ArgoCD creates this object after the pull request merges.</p>
			{:else if restart.length > 0}
				<p class="flex items-start gap-2">
					<TriangleAlert size={14} class="mt-0.5 shrink-0 text-warn-ink" />
					<span>
						<b class="font-medium">Restart required:</b>
						{restart.map((c) => c.field).join(', ')} appl{restart.length > 1 ? 'y' : 'ies'} at the next
						power cycle{#if vm?.power === 'On'}; the VM keeps running until you restart it{/if}.
					</span>
				</p>
			{:else}
				<p>Applies without a restart once the pull request merges and syncs.</p>
			{/if}
			{#if delta && vm?.nodeName}
				<p>
					Capacity: {delta} on <b class="font-medium">{vm.nodeName}</b> once applied.
				</p>
			{/if}
		</div>
	</section>

	{#if item.yaml}
		<details class="rounded border border-line">
			<summary
				class="cursor-pointer border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
				>{item.baseYAML ? 'Manifest diff' : 'Manifest'}</summary
			>
			<ManifestDiff before={item.baseYAML} after={item.yaml} />
		</details>
	{/if}

	{#snippet footer()}
		<ErrorNote error={proposeOp.error || unstageOp.error} class="mb-2" />
		<div class="mb-2 flex items-center gap-3">
			<GitOpsStepper stage="staged" />
			<span class="ml-auto text-[11px] text-ink-faint">
				Opens one pull request for the {count} staged change{count > 1 ? 's' : ''} in {project}.
				Blank fields use the text shown; Tab takes it to edit.
			</span>
		</div>
		<div class="flex items-start gap-2">
			<TextInput
				bind:value={title}
				suggest={lane?.defaultTitle}
				placeholder="Pull request title"
				aria-label="Pull request title"
				class="flex-1"
			/>
			<Button onclick={propose} disabled={proposeOp.busy}>
				{proposeOp.busy ? 'Proposing…' : 'Propose pull request'}
			</Button>
		</div>
		<TextArea
			bind:value={message}
			suggest={lane?.defaultBody}
			placeholder="Description (optional; the change summary is appended)"
			aria-label="Pull request description"
			class="mt-2"
		/>
	{/snippet}
</ReviewPane>
