<script lang="ts">
	import { tick, type Snippet } from 'svelte';

	// One "Set up for you" line: what was chosen, why, and Change. Change
	// opens the row's editor in place (a blue bar marks it); Done or Escape
	// folds it back and returns focus to Change. open is owned by the card,
	// so only one editor is open at a time.
	let {
		label,
		open = false,
		onopen,
		onclose,
		value,
		sub,
		editor,
	}: {
		label: string;
		open?: boolean;
		onopen: () => void;
		onclose: () => void;
		value: Snippet;
		sub?: Snippet;
		editor?: Snippet<[() => void]>;
	} = $props();

	let button = $state<HTMLButtonElement>();
	let body = $state<HTMLDivElement>();
	// An opened editor takes focus at its first control (a picker's search).
	$effect(() => {
		if (open && body)
			body
				.querySelector<HTMLElement>(
					'input:not([disabled]), select, textarea, button:not([disabled])',
				)
				?.focus();
	});
	async function done() {
		onclose();
		await tick();
		button?.focus();
	}
</script>

{#if open && editor}
	<!-- Escape from any control inside folds the editor, not the sheet. -->
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<div
		class="grid grid-cols-[92px_minmax(0,1fr)] items-start gap-x-3 border-t border-line-soft px-card py-3.5 shadow-[inset_3px_0_0_var(--color-accent)] first:border-t-0"
		role="group"
		aria-label={label}
		onkeydown={(e) => {
			if (e.key === 'Escape' && !e.defaultPrevented) {
				e.preventDefault();
				done();
			}
		}}
	>
		<span class="pt-2 text-[13px] text-ink-muted">{label}</span>
		<div class="min-w-0" bind:this={body}>{@render editor(done)}</div>
	</div>
{:else}
	<div
		class="grid grid-cols-[92px_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-t border-line-soft px-card py-2 first:border-t-0"
	>
		<span class="text-[13px] text-ink-muted">{label}</span>
		<span class="flex min-w-0 flex-col text-sm text-ink">
			<span class="min-w-0 truncate">{@render value()}</span>
			{#if sub}
				<span class="flex min-w-0 items-center gap-1.5 text-[13px] leading-[18px] text-ink-faint"
					>{@render sub()}</span
				>
			{/if}
		</span>
		{#if editor}
			<button
				bind:this={button}
				type="button"
				onclick={onopen}
				aria-label="Change {label.toLowerCase()}"
				class="h-[30px] rounded-md border border-line bg-panel px-3 text-[13px] text-ink hover:border-ink-faint"
				>Change</button
			>
		{/if}
	</div>
{/if}
