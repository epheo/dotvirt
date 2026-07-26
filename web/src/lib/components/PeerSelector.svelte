<script lang="ts">
	// Key=value label-selector pair shared by the firewall modals, both as the
	// "applies to" panel (flex-1 inputs, the default) and inline in rule rows
	// (fixed widths via keyClass/valueClass). Renders as a fragment so the
	// parent's flex row owns layout and gaps.
	let {
		key = $bindable(''),
		value = $bindable(''),
		keyPlaceholder = '',
		valuePlaceholder = '',
		keyClass = 'min-w-0 flex-1',
		valueClass = 'min-w-0 flex-1',
	}: {
		key?: string;
		value?: string;
		keyPlaceholder?: string;
		valuePlaceholder?: string;
		keyClass?: string;
		valueClass?: string;
	} = $props();
</script>

<input
	bind:value={key}
	placeholder={keyPlaceholder}
	class="{keyClass} rounded border border-line-strong px-2 py-1 text-xs"
/>
<span class="text-ink-faint">=</span>
<input
	bind:value
	placeholder={valuePlaceholder}
	class="{valueClass} rounded border border-line-strong px-2 py-1 text-xs"
/>
