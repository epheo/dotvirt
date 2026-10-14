<script lang="ts">
	import type { Options } from '$lib/api';
	import FormField from '../FormField.svelte';
	import SelectInput from '../SelectInput.svelte';

	let {
		instancetype = $bindable(),
		running = $bindable(),
		options,
		nudged,
	}: {
		instancetype: string;
		running: boolean;
		options: Options | null;
		nudged: boolean;
	} = $props();
</script>

<div class="space-y-4">
	<p class="text-xs text-ink-muted">
		Choose a size (instance type) and whether to power the VM on after creation.
	</p>
	<FormField label="Size (instancetype)" error={nudged && !instancetype ? 'Size is required' : ''}>
		<SelectInput bind:value={instancetype}>
			{#each options?.instancetypes ?? [] as it (it.name)}
				<option value={it.name}>{it.name} — {it.cpu} CPU / {it.memory}</option>
			{/each}
		</SelectInput>
	</FormField>
	<label class="flex items-center gap-2">
		<input type="checkbox" bind:checked={running} />
		<span class="text-ink-soft">Start immediately (runStrategy: Always)</span>
	</label>
</div>
