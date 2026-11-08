<script lang="ts">
	import { api } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { TONE_TEXT } from '#lib/status.js';
	import Button from './Button.svelte';

	// Discarding a project's draft asks once, in place: the draft is the
	// user's unproposed work. Each lane mounts its own, so one lane's discard
	// never shows busy on another.
	let { project, count, onclose }: { project: string; count: number; onclose: () => void } =
		$props();

	const op = action();
	async function discard() {
		if (!(await op.run(() => api.discardDraft(project)))) return;
		onclose();
		drafts.refresh();
		ui.showToast(`Discarded the staged changes in ${project}.`, { kind: 'success' });
	}
</script>

<div class="flex flex-wrap items-center gap-x-3 gap-y-1">
	<span class="text-ink">Discard {count} staged change{count === 1 ? '' : 's'}?</span>
	<Button size="sm" variant="danger" disabled={op.busy} onclick={discard}
		>{op.busy ? 'Discarding...' : 'Discard'}</Button
	>
	<button
		class="text-ink-soft hover:underline disabled:text-ink-faint"
		disabled={op.busy}
		onclick={onclose}>Keep</button
	>
	{#if op.error}<p class="basis-full {TONE_TEXT.danger}">{op.error}</p>{/if}
</div>
