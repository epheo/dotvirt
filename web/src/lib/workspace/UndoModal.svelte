<script lang="ts">
	import { TriangleAlert } from '@lucide/svelte';
	import { api, type Commit, type ProposeResult } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts, pruneKey, pruneTotal } from '#lib/state/drafts.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import NextStepButton from '#lib/components/NextStepButton.svelte';
	import Note from '#lib/components/Note.svelte';
	import PruneAck from '#lib/components/PruneAck.svelte';

	// Undo asks once, in the app's confirm dialog, then opens the forward-commit
	// pull request. Nothing changes until that PR merges.
	let {
		project,
		hash,
		commit,
		warning,
		onclose,
		onreverted,
	}: {
		project: string;
		hash: string;
		// May still be loading; the title then stays blank.
		commit?: Commit;
		// The review's revert warning, when it has one.
		warning?: string;
		onclose: () => void;
		onreverted: (r: ProposeResult) => void;
	} = $props();

	// Its merge prunes like any other: what the project's lane says Argo would
	// delete needs the same yes, held to the set it was given to.
	const lane = $derived(drafts.drafts.find((d) => d.project === project)?.draft);
	const prune = $derived(lane?.prune ?? []);
	const pruneHidden = $derived(lane?.pruneHidden ?? 0);
	const pruning = $derived(pruneTotal(lane) > 0);
	let ack = $state(false);
	const acked = $derived(pruneKey(lane));
	$effect(() => {
		void acked;
		ack = false;
	});

	const op = action();
	async function undo() {
		if (op.busy || (pruning && !ack)) return;
		const ok = await op.run(async () =>
			onreverted(await api.revert(project, hash, pruning ? { prune, pruneHidden } : undefined)),
		);
		if (ok) onclose();
		drafts.refresh();
	}
</script>

<Modal title="Undo this change?" danger {onclose}>
	<div class="space-y-2 px-5 py-4 text-sm text-ink-soft">
		<p>
			<b class="font-medium text-ink">{commit?.title}</b>
			in {project}
			<code class="text-xs text-ink-faint">{commit?.shortHash}</code>
		</p>
		<p>
			Opens a pull request restoring every file this change touched to its previous state. Nothing
			changes until it is approved and merged in the forge.
		</p>
		{#if warning}
			<Note tone="warn" class="flex items-start gap-2">
				<TriangleAlert size={14} class="mt-0.5 shrink-0" />
				<span>{warning}</span>
			</Note>
		{/if}
		{#if pruning}<PruneAck {prune} hidden={pruneHidden} bind:ack />{/if}
		<ErrorNote error={op.error} />
		<NextStepButton next={op.next} {project} />
	</div>
	{#snippet footer()}
		<Button variant="secondary" class="ml-auto" onclick={onclose}>Cancel</Button>
		<Button variant="danger" onclick={undo} disabled={op.busy || (pruning && !ack)}>
			{op.busy ? 'Opening...' : 'Open pull request'}
		</Button>
	{/snippet}
</Modal>
