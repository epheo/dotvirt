<!-- V admits null: a cleared <input type="number"> binds null, and callers
     deliberately type such fields number | null so .trim() can't be called
     on them by mistake. -->
<script lang="ts" generics="V extends string | number | null">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { acceptsSuggestion } from '$lib/suggest';

	// The canonical modal text/number field: one home for the border, padding
	// and focus treatment, so dialogs stop hand-rolling drifting class strings.
	// size 'sm' is the compact table/toolbar variant. Everything else (type,
	// min/max, list, placeholder, data-autofocus) passes through as native
	// attributes. width is the wrapper's width utility (full by default: the
	// modal-form case); class is for the rest of its layout (ml-auto).
	// suggest is a default the empty field falls back to: shown as the
	// placeholder, taken as the value on Tab. placeholder alone is a hint.
	let {
		value = $bindable(),
		mono = false,
		size = 'md',
		width = 'w-full',
		suggest,
		placeholder,
		class: cls = '',
		onkeydown,
		...rest
	}: {
		value?: V;
		mono?: boolean;
		size?: 'md' | 'sm';
		width?: string;
		suggest?: string;
		class?: string;
	} & Omit<HTMLInputAttributes, 'value' | 'class' | 'size' | 'width'> = $props();
</script>

<span class="relative block {width} {cls}">
	<input
		bind:value
		placeholder={suggest || placeholder}
		onkeydown={(e) => {
			if (acceptsSuggestion(e, value, suggest)) value = suggest as V;
			else onkeydown?.(e);
		}}
		class="peer w-full rounded border border-line-strong {size === 'sm'
			? 'px-2 py-1 text-xs'
			: 'px-2 py-1.5 text-sm'} focus:border-accent/60 disabled:bg-inset-strong disabled:text-ink-faint {mono
			? 'font-mono'
			: ''}"
		{...rest}
	/>
	{#if suggest}
		<kbd
			class="pointer-events-none absolute top-1/2 right-1.5 -translate-y-1/2 rounded border border-line-strong bg-inset px-1 font-sans text-[10px] leading-4 text-ink-muted opacity-0 peer-focus:peer-placeholder-shown:opacity-100"
			>Tab</kbd
		>
	{/if}
</span>
