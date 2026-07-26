<script lang="ts">
	import type { HTMLSelectAttributes } from 'svelte/elements';
	import type { StorageClass } from '$lib/api';
	import SelectInput from './SelectInput.svelte';

	// The one storage-class picker. The empty option means "no explicit class"
	// and each site names that meaning (cluster default vs keep); exclude hides
	// a disk's current class so a migration cannot target itself.
	let {
		options,
		value = $bindable(),
		emptyLabel = 'cluster default',
		exclude = '',
		class: cls = '',
		...rest
	}: {
		options: StorageClass[];
		value?: string;
		emptyLabel?: string;
		exclude?: string;
		class?: string;
	} & Omit<HTMLSelectAttributes, 'value' | 'class'> = $props();
</script>

<SelectInput bind:value class={cls} {...rest}>
	<option value="">{emptyLabel}</option>
	{#each options as sc (sc.name)}
		{#if sc.name !== exclude}
			<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
		{/if}
	{/each}
</SelectInput>
