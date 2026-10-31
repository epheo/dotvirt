<script lang="ts">
	import { X } from '@lucide/svelte';
	import type { Options } from '#lib/api.js';
	import type { RowList } from '#lib/rowlist.svelte.js';
	import Button from '../Button.svelte';
	import FormField from '../FormField.svelte';
	import StorageClassSelect from '../StorageClassSelect.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		diskSize = $bindable(),
		storageClass = $bindable(),
		disks,
		options,
	}: {
		diskSize: string;
		storageClass: string; // '' = cluster default
		disks: RowList<{ name: string; size: string; storageClass: string }>;
		options: Options | null;
	} = $props();
</script>

<div class="space-y-4">
	<p class="text-xs text-ink-muted">Configure the boot disk and any additional disks.</p>
	<div class="grid grid-cols-2 gap-4">
		<FormField label="Root disk size">
			<TextInput bind:value={diskSize} />
		</FormField>
		<FormField label="Storage class">
			<StorageClassSelect options={options?.storageClasses ?? []} bind:value={storageClass} />
		</FormField>
	</div>
	<div>
		<div class="mb-1 flex items-center justify-between">
			<span class="text-ink-soft">Extra disks</span>
			<Button variant="link" size="sm" onclick={disks.add}>+ Add disk</Button>
		</div>
		{#each disks.rows as disk, i (i)}
			<div class="mb-1 flex gap-2">
				<TextInput bind:value={disk.name} placeholder="name" class="min-w-0 flex-1" />
				<TextInput bind:value={disk.size} suggest="10Gi" width="w-20" />
				<StorageClassSelect
					options={options?.storageClasses ?? []}
					bind:value={disk.storageClass}
					class="min-w-0 flex-1"
				/>
				<button
					onclick={() => disks.remove(i)}
					type="button"
					aria-label="Remove disk"
					class="text-danger hover:text-danger-ink"><X size={14} /></button
				>
			</div>
		{/each}
	</div>
</div>
