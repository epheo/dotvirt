<script lang="ts">
	import TextInput from './TextInput.svelte';

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

<TextInput bind:value={key} size="sm" placeholder={keyPlaceholder} class={keyClass} />
<span class="text-ink-faint">=</span>
<TextInput bind:value size="sm" placeholder={valuePlaceholder} class={valueClass} />
