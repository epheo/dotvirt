<!-- V admits null: a cleared <input type="number"> binds null, and callers
     deliberately type such fields number | null so .trim() can't be called
     on them by mistake. -->
<script lang="ts" generics="V extends string | number | null">
	import type { HTMLInputAttributes } from 'svelte/elements';

	// The canonical modal text/number field: one home for the border, padding
	// and focus treatment, so dialogs stop hand-rolling drifting class strings.
	// Everything else (type, min/max, list, placeholder, data-autofocus) passes
	// through as native attributes.
	let {
		value = $bindable(),
		mono = false,
		class: cls = '',
		...rest
	}: { value?: V; mono?: boolean; class?: string } & Omit<
		HTMLInputAttributes,
		'value' | 'class'
	> = $props();
</script>

<input
	bind:value
	class="w-full rounded border border-line-strong px-2 py-1.5 text-sm focus:border-accent/60 disabled:bg-inset-strong disabled:text-ink-faint {mono
		? 'font-mono'
		: ''} {cls}"
	{...rest}
/>
