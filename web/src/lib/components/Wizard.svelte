<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Check } from 'lucide-svelte';
	import Modal from './Modal.svelte';

	// A vCenter-style wizard scaffold: a left step-rail, one panel at a time, and a
	// Back/Next/Finish footer. Navigation is deliberately *free* — every step is
	// reachable at any time (click the rail, or Back/Next as a convenience); the
	// per-step `valid` flag only drives the rail marker, it never traps the user.
	// The single hard gate is `canFinish`, which disables Finish on the last step.
	// The parent owns all form state and the validity derivations; this component
	// owns only navigation + chrome, so it stays reusable by other create flows.
	type WizardStep = {
		title: string;
		valid?: boolean; // undefined ⇒ optional step (no required fields)
		body: Snippet;
	};

	let {
		title,
		steps,
		current = $bindable(0),
		canFinish = false,
		submitting = false,
		error = '',
		finishLabel = 'Finish',
		footerHint = '',
		icon,
		onfinish,
		onclose,
	}: {
		title: string;
		steps: WizardStep[];
		current?: number;
		canFinish?: boolean;
		submitting?: boolean;
		error?: string;
		finishLabel?: string;
		footerHint?: string;
		icon?: Snippet;
		onfinish: () => void;
		onclose: () => void;
	} = $props();

	const last = $derived(current === steps.length - 1);

	function go(i: number) {
		current = i;
	}
	function next() {
		if (current < steps.length - 1) current++;
	}
	function back() {
		if (current > 0) current--;
	}

	// Rail badge: current = blue number · invalid = amber number · satisfied
	// required step = green check · optional step = slate number.
	function railBadge(step: WizardStep, i: number): { cls: string; text: string; done?: boolean } {
		if (i === current) return { cls: 'bg-blue-500 text-white', text: String(i + 1) };
		if (step.valid === false)
			return { cls: 'bg-amber-100 text-amber-700 ring-1 ring-amber-300', text: String(i + 1) };
		if (step.valid === true) return { cls: 'bg-green-500 text-white', text: '', done: true };
		return { cls: 'bg-slate-200 text-slate-500', text: String(i + 1) };
	}
</script>

<Modal {title} size="3xl" {icon} {onclose}>
	<div class="flex min-h-0 flex-1">
		<!-- Step rail: every item is clickable (free navigation). -->
		<nav
			class="w-52 shrink-0 space-y-0.5 overflow-y-auto border-r border-slate-200 bg-slate-50/60 p-2"
		>
			{#each steps as step, i (i)}
				{@const b = railBadge(step, i)}
				<button
					type="button"
					onclick={() => go(i)}
					class="flex w-full items-center gap-2.5 rounded px-2.5 py-1.5 text-left text-sm {i ===
					current
						? 'bg-blue-50 font-medium text-blue-700'
						: 'text-slate-600 hover:bg-slate-100'}"
				>
					<span
						class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[11px] {b.cls}"
						>{#if b.done}<Check size={12} />{:else}{b.text}{/if}</span
					>
					<span class="truncate">{step.title}</span>
				</button>
			{/each}
		</nav>

		<!-- Active step body. -->
		<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm">
			{#if steps[current]}{@render steps[current].body()}{/if}
		</div>
	</div>

	{#if error}
		<pre
			class="mx-5 mb-1 rounded bg-red-50 p-2 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
	{/if}
	{#snippet footer()}
		{#if footerHint}<span class="text-xs text-slate-400">{footerHint}</span>{/if}
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Cancel</button
		>
		<button
			onclick={back}
			disabled={current === 0}
			class="rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100 disabled:text-slate-300"
			>Back</button
		>
		{#if last}
			<button
				onclick={onfinish}
				disabled={!canFinish || submitting}
				class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
				>{finishLabel}</button
			>
		{:else}
			<button onclick={next} class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white"
				>Next</button
			>
		{/if}
	{/snippet}
</Modal>
