<script lang="ts">
	import { untrack } from 'svelte';
	import type { HTMLSelectAttributes } from 'svelte/elements';
	import type { StorageClass, StorageClassInfo } from '#lib/api.js';
	import { noClassAllowed, offeredClasses, pinnedClass, repick } from '#lib/storage.js';
	import SelectInput from './SelectInput.svelte';

	// The one storage-class picker. The empty option means "no explicit class"
	// and each site names that meaning (cluster default vs keep); exclude hides
	// a disk's current class so a migration cannot target itself. Classes the
	// namespace's quotas forbid are not offered. pin, where the empty option is
	// the cluster default, moves the pick off a forbidden class and drops that
	// option when the default itself is forbidden.
	let {
		options,
		value = $bindable(),
		emptyLabel = 'cluster default',
		exclude = '',
		pin = true,
		class: cls = '',
		...rest
	}: {
		options: (StorageClass | StorageClassInfo)[];
		value?: string;
		emptyLabel?: string;
		exclude?: string;
		pin?: boolean;
		class?: string;
	} & Omit<HTMLSelectAttributes, 'value' | 'class' | 'size'> = $props();

	const shown = $derived(offeredClasses(options));
	const defaultForbidden = $derived(pin && !!pinnedClass(options));
	$effect(() => {
		if (!pin) return;
		const next = repick(options, value ?? '');
		if (next !== null) untrack(() => (value = next));
	});
</script>

<SelectInput bind:value class={cls} {...rest}>
	{#if !defaultForbidden}<option value="">{emptyLabel}</option>{/if}
	{#each shown as sc (sc.name)}
		{#if sc.name !== exclude}
			<option value={sc.name}>{sc.name}{sc.default ? ' (default)' : ''}</option>
		{/if}
	{/each}
</SelectInput>
{#if noClassAllowed(options)}
	<span class="text-xs text-warn-ink">No storage class may be used in this namespace.</span>
{/if}
