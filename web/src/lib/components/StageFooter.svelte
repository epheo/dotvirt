<script lang="ts">
	// The staging dialogs' shared footer row: the changeset hint, Cancel, and a
	// primary action that reads its busy label while the request is in flight.
	// Rendered inside Modal's footer bar.
	let {
		label,
		busyLabel = 'Staging…',
		hint = 'Stages to Changes — applies when the project’s PR merges.',
		disabled = false,
		submitting = false,
		onsubmit,
		oncancel,
	}: {
		label: string;
		busyLabel?: string;
		hint?: string;
		disabled?: boolean;
		submitting?: boolean;
		onsubmit: () => void;
		oncancel: () => void;
	} = $props();
</script>

<span class="text-xs text-ink-faint">{hint}</span>
<button
	onclick={oncancel}
	class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-slate-100">Cancel</button
>
<button
	onclick={onsubmit}
	disabled={disabled || submitting}
	class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
>
	{submitting ? busyLabel : label}
</button>
