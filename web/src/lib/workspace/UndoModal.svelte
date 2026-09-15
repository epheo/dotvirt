<script lang="ts">
	import { TriangleAlert } from 'lucide-svelte';
	import { api, type Commit, type ProposeResult } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import { drafts } from '$lib/state/drafts.svelte';
	import Button from '$lib/components/Button.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import Note from '$lib/components/Note.svelte';

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

	const op = action();
	async function undo() {
		if (op.busy) return;
		const ok = await op.run(async () => onreverted(await api.revert(project, hash)));
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
		<ErrorNote error={op.error} />
	</div>
	{#snippet footer()}
		<Button variant="secondary" class="ml-auto" onclick={onclose}>Cancel</Button>
		<Button variant="danger" onclick={undo} disabled={op.busy}>
			{op.busy ? 'Opening…' : 'Open pull request'}
		</Button>
	{/snippet}
</Modal>
