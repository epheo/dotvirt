<script lang="ts">
	import { X } from '@lucide/svelte';
	import type { EditForm } from '#lib/editform.js';
	import Button from '../Button.svelte';
	import TextInput from '../TextInput.svelte';

	let { form = $bindable() }: { form: EditForm } = $props();
</script>

<div class="mb-2 flex items-center justify-between">
	<span class="text-xs text-ink-faint">Key/value metadata.</span>
	<Button
		variant="link"
		size="sm"
		onclick={() => (form.labelRows = [...form.labelRows, { key: '', value: '' }])}
		>+ Add label</Button
	>
</div>
{#each form.labelRows as row, i (i)}
	<div class="mb-1 flex gap-2">
		<TextInput bind:value={row.key} size="sm" class="w-1/2!" placeholder="key" />
		<TextInput bind:value={row.value} size="sm" class="w-1/2!" placeholder="value" />
		<button
			onclick={() => (form.labelRows = form.labelRows.filter((_, idx) => idx !== i))}
			aria-label="Remove label"
			class="text-danger"><X size={14} /></button
		>
	</div>
{/each}
{#if form.labelRows.length === 0}<p class="text-xs text-ink-faint">No labels.</p>{/if}
