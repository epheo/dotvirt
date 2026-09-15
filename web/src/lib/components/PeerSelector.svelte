<script lang="ts">
	import TextInput from './TextInput.svelte';

	// Key=value label-selector pair shared by the firewall modals, both as the
	// "applies to" panel (flex-1 inputs, the default) and inline in rule rows
	// (fixed widths via keyWidth/valueWidth). Renders as a fragment so the
	// parent's flex row owns layout and gaps.
	let {
		key = $bindable(''),
		value = $bindable(''),
		keyPlaceholder = '',
		valuePlaceholder = '',
		keyWidth = 'min-w-0 flex-1',
		valueWidth = 'min-w-0 flex-1',
	}: {
		key?: string;
		value?: string;
		keyPlaceholder?: string;
		valuePlaceholder?: string;
		keyWidth?: string;
		valueWidth?: string;
	} = $props();
</script>

<TextInput bind:value={key} size="sm" placeholder={keyPlaceholder} width={keyWidth} />
<span class="text-ink-faint">=</span>
<TextInput bind:value size="sm" placeholder={valuePlaceholder} width={valueWidth} />
