<script lang="ts">
	import type { Snippet } from 'svelte';
	import { X } from 'lucide-svelte';

	// The one dialog shell: overlay, backdrop-click + Escape dismissal, and the
	// title bar every modal shares. Callers own the body markup (padding and
	// scroll behavior vary by dialog) and pass footer content into the standard
	// bottom bar.
	let {
		title,
		size = 'md',
		danger = false,
		dismissable = true,
		icon,
		onclose,
		children,
		footer,
	}: {
		title: string;
		size?: 'md' | 'lg' | '3xl';
		// Destructive dialogs render the title in red.
		danger?: boolean;
		// false pins the dialog open — backdrop, Escape, and the X button all
		// refuse to close (e.g. mid-upload, where closing would kill the stream).
		dismissable?: boolean;
		icon?: Snippet;
		onclose: () => void;
		children: Snippet;
		footer?: Snippet;
	} = $props();

	const width = $derived({ md: 'max-w-md', lg: 'max-w-lg', '3xl': 'max-w-3xl' }[size]);
	function dismiss() {
		if (dismissable) onclose();
	}
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && dismiss()}
	onkeydown={(e) => e.key === 'Escape' && dismiss()}
	role="presentation"
>
	<div class="flex max-h-[90vh] w-full {width} flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2
				class="flex items-center gap-2 text-base font-semibold {danger
					? 'text-red-700'
					: 'text-slate-800'}"
			>
				{#if icon}{@render icon()}{/if}{title}
			</h2>
			<button
				onclick={dismiss}
				aria-label="Close"
				disabled={!dismissable}
				class="text-slate-400 hover:text-slate-700 disabled:opacity-40"><X size={18} /></button
			>
		</header>
		{@render children()}
		{#if footer}
			<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
				{@render footer()}
			</footer>
		{/if}
	</div>
</div>
