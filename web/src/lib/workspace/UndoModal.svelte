<script lang="ts">
	import { TriangleAlert } from '@lucide/svelte';
	import { api, HttpError, type Commit, type ProposeResult, type PruneSet } from '#lib/api.js';
	import { friendlyError } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts, PruneYes, pruneTotal } from '#lib/state/drafts.svelte.js';
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

	// Its merge prunes like any other: what reverting lets Argo delete beyond
	// its own restores needs the same yes. Read live for this commit, never the
	// draft's set (the draft is no part of the revert), and again after a
	// refusal.
	let pending = $state<PruneSet | null>(null);
	let pendingError = $state('');
	async function loadPending() {
		pendingError = '';
		try {
			pending = await api.revertPrune(project, hash);
		} catch (e) {
			pendingError = friendlyError(e);
		}
	}
	$effect(() => {
		void `${project}@${hash}`;
		pending = null;
		loadPending();
	});
	const prune = $derived(pending?.prune ?? []);
	const pruneHidden = $derived(pending?.pruneHidden ?? 0);
	const pruning = $derived(pruneTotal(pending) > 0);
	const yes = new PruneYes(() => pending);
	const blocked = $derived(!pending || (pruning && !yes.ack));

	const op = action();
	async function undo() {
		if (op.busy || blocked) return;
		const ok = await op.run(async () => {
			const r = await api
				.revert(project, hash, pruning ? { prune, pruneHidden } : undefined)
				.catch((e: unknown) => {
					// The prune may have moved since it was read: the re-read set,
					// if it differs, asks for a new yes.
					if (e instanceof HttpError && e.status === 409) loadPending();
					throw e;
				});
			onreverted(r);
		});
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
		{#if pruning}<PruneAck {prune} hidden={pruneHidden} bind:ack={yes.ack} />{/if}
		<ErrorNote error={pendingError} />
		<ErrorNote error={op.error} />
		<NextStepButton next={op.next} {project} />
	</div>
	{#snippet footer()}
		<Button variant="secondary" class="ml-auto" onclick={onclose}>Cancel</Button>
		<Button variant="danger" onclick={undo} disabled={op.busy || blocked}>
			{op.busy ? 'Opening...' : 'Open pull request'}
		</Button>
	{/snippet}
</Modal>
