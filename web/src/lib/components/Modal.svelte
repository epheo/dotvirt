<script lang="ts">
	import type { Snippet } from 'svelte';
	import { X } from 'lucide-svelte';

	// The one dialog shell: overlay, backdrop-click + Escape dismissal, focus
	// containment, and the title bar every modal shares. Callers own the body
	// markup (padding and scroll behavior vary by dialog) and pass footer
	// content into the standard bottom bar.
	let {
		title,
		subtitle = '',
		size = 'md',
		danger = false,
		dismissable = true,
		icon,
		onclose,
		children,
		footer,
	}: {
		title: string;
		// Muted suffix after the title (the NSX · vSphere vocabulary pairs).
		subtitle?: string;
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

	const titleId = $props.id();
	const width = $derived({ md: 'max-w-md', lg: 'max-w-lg', '3xl': 'max-w-3xl' }[size]);
	function dismiss() {
		if (dismissable) onclose();
	}

	let panel = $state<HTMLDivElement>();

	const FOCUSABLE =
		'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

	// Focus lands inside on open ([data-autofocus] → first focusable → the
	// panel itself) and returns to the opener on close.
	$effect(() => {
		if (!panel) return;
		const opener = document.activeElement as HTMLElement | null;
		const target =
			panel.querySelector<HTMLElement>('[data-autofocus]') ??
			panel.querySelector<HTMLElement>(FOCUSABLE) ??
			panel;
		target.focus();
		return () => opener?.focus();
	});

	// Tab wraps inside the panel. A hand-rolled trap on purpose: <dialog>'s top
	// layer would paint over the toast host and take over stacking of nested
	// dialogs (ConfirmDelete over EditSettings).
	function trapTab(e: KeyboardEvent) {
		if (e.key !== 'Tab' || !panel) return;
		const items = [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
			(el) => el.offsetParent !== null,
		);
		if (items.length === 0) return;
		const first = items[0];
		const last = items[items.length - 1];
		const active = document.activeElement;
		if (e.shiftKey && (active === first || active === panel)) {
			e.preventDefault();
			last.focus();
		} else if (!e.shiftKey && active === last) {
			e.preventDefault();
			first.focus();
		}
	}
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && dismiss()}
	onkeydown={(e) => e.key === 'Escape' && dismiss()}
	role="presentation"
>
	<div
		bind:this={panel}
		role="dialog"
		aria-modal="true"
		aria-labelledby={titleId}
		tabindex="-1"
		onkeydown={trapTab}
		class="flex max-h-[90vh] w-full {width} flex-col rounded-lg bg-panel shadow-xl outline-none"
	>
		<header class="flex items-center justify-between border-b border-line px-5 py-3">
			<h2
				id={titleId}
				class="flex items-center gap-2 text-base font-semibold {danger
					? 'text-danger-ink'
					: 'text-ink'}"
			>
				{#if icon}{@render icon()}{/if}{title}
				{#if subtitle}<span class="font-normal text-ink-faint">· {subtitle}</span>{/if}
			</h2>
			<button
				onclick={dismiss}
				aria-label="Close"
				disabled={!dismissable}
				class="text-ink-faint hover:text-ink-soft disabled:opacity-40"><X size={18} /></button
			>
		</header>
		{@render children()}
		{#if footer}
			<footer class="flex items-center gap-2 border-t border-line px-5 py-3">
				{@render footer()}
			</footer>
		{/if}
	</div>
</div>
