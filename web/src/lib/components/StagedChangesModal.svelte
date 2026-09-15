<script lang="ts">
	import type { DraftItem } from '$lib/api';
	import ChangeList from './ChangeList.svelte';
	import Button from './Button.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import Modal from './Modal.svelte';

	// Per-VM view of a staged change: the diff + discard / open-in-Changes.
	let {
		item,
		busy = false,
		onclose,
		ondiscard,
		onreview,
	}: {
		item: DraftItem;
		busy?: boolean;
		onclose: () => void;
		ondiscard: () => void;
		onreview: () => void;
	} = $props();

	const isDelete = $derived(item.kind === 'delete');
</script>

<Modal title="Staged changes — {item.name}" {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm">
		<div class="mb-2 flex items-center justify-between gap-3">
			<p class="text-xs text-ink-muted">{item.namespace}/{item.name} · not yet proposed</p>
			<GitOpsStepper stage="staged" />
		</div>
		{#if isDelete}
			<p class="text-ink-soft">
				This VM is <strong>staged for removal</strong> — it'll be deleted from the cluster when the pull
				request merges.
			</p>
		{:else}
			<ChangeList changes={item.changes} />
		{/if}
	</div>
	{#snippet footer()}
		<Button variant="secondary" onclick={ondiscard} disabled={busy}>Discard</Button>
		<Button class="ml-auto" onclick={onreview}>Review &amp; propose →</Button>
	{/snippet}
</Modal>
