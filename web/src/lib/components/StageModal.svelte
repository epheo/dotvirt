<script lang="ts">
	import type { Snippet } from 'svelte';
	import { friendlyError } from '$lib/format';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	// The staging dialogs' shared shell: Modal + scrollable body + ErrorNote +
	// StageFooter, owning the submit try/catch every dialog repeated. The
	// form-specific missing/summary derivations stay in each dialog — they ARE
	// the form; this owns only what happens around them. onsubmit stages the
	// request; success reports to onstaged then closes.
	let {
		title,
		size = 'md',
		label,
		missing = [],
		summary = '',
		onsubmit,
		onstaged,
		onclose,
		icon,
		children,
	}: {
		title: string;
		size?: 'md' | 'lg' | '3xl';
		label: string;
		missing?: string[];
		summary?: string;
		onsubmit: () => Promise<void>;
		onstaged: () => void;
		onclose: () => void;
		icon?: Snippet;
		children: Snippet;
	} = $props();

	let submitting = $state(false);
	let error = $state('');

	async function submit() {
		if (missing.length) return;
		submitting = true;
		error = '';
		try {
			await onsubmit();
			onstaged();
			onclose();
		} catch (e) {
			error = friendlyError(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal {title} {size} {onclose} {icon}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		{@render children()}
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			{label}
			{summary}
			{missing}
			disabled={missing.length > 0}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
