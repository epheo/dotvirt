<script lang="ts">
	// The staging dialogs' shared footer row: one always-meaningful line
	// (why the action is disabled > what will be staged > the generic hint),
	// then Cancel and the primary action with its busy label.
	// Rendered inside Modal's footer bar.
	let {
		label,
		busyLabel = 'Staging…',
		hint = 'Stages to Changes — applies when the project’s PR merges.',
		summary = '',
		missing = [],
		disabled = false,
		submitting = false,
		onsubmit,
		oncancel,
	}: {
		label: string;
		busyLabel?: string;
		hint?: string;
		// What this exact submission stages, derived from the request payload.
		summary?: string;
		// Unmet requirements; the first one explains a disabled button.
		missing?: string[];
		disabled?: boolean;
		submitting?: boolean;
		onsubmit: () => void;
		oncancel: () => void;
	} = $props();
</script>

{#if disabled && missing.length}
	<span class="min-w-0 truncate text-xs text-warn-ink" title={missing.join(' · ')}
		>{missing[0]}{missing.length > 1 ? ` (+${missing.length - 1} more)` : ''}</span
	>
{:else if summary}
	<span class="min-w-0 truncate text-xs text-ink-muted" title={summary}>{summary}</span>
{:else}
	<span class="text-xs text-ink-faint">{hint}</span>
{/if}
<button
	onclick={oncancel}
	class="ml-auto shrink-0 rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong"
	>Cancel</button
>
<button
	onclick={onsubmit}
	disabled={disabled || submitting}
	class="shrink-0 rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
>
	{submitting ? busyLabel : label}
</button>
