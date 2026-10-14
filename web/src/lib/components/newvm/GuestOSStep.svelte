<script lang="ts">
	import type { Options } from '#lib/api.js';
	import FormField from '../FormField.svelte';
	import SelectInput from '../SelectInput.svelte';

	let {
		osImage = $bindable(),
		preference = $bindable(),
		options,
		nudged,
	}: {
		osImage: string; // "name|namespace"
		preference: string;
		options: Options | null;
		nudged: boolean;
	} = $props();
</script>

<div class="space-y-4">
	<p class="text-xs text-ink-muted">
		Select the OS image to boot from and a preference that tunes the VM for that guest.
	</p>
	<FormField label="OS image" error={nudged && !osImage ? 'OS image is required' : ''}>
		<SelectInput bind:value={osImage}>
			{#each (options?.osImages ?? []).filter((i) => i.ready) as img (img.namespace + img.name)}
				<option value={`${img.name}|${img.namespace}`}>{img.name}</option>
			{/each}
		</SelectInput>
	</FormField>
	<FormField
		label="Preference (OS tuning)"
		error={nudged && !preference ? 'Preference is required' : ''}
	>
		<SelectInput bind:value={preference}>
			{#each options?.preferences ?? [] as p (p.name)}
				<option value={p.name}>{p.displayName || p.name}</option>
			{/each}
		</SelectInput>
	</FormField>
</div>
