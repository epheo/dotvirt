<script lang="ts">
	import type { Options } from '#lib/api.js';
	import type { DiskRow, EditForm } from '#lib/editform.js';
	import RowListEditor from '../RowListEditor.svelte';
	import StorageClassSelect from '../StorageClassSelect.svelte';
	import TextInput from '../TextInput.svelte';

	let { form = $bindable(), options }: { form: EditForm; options: Options | null } = $props();

	function addDisk() {
		form.disks = [
			...form.disks,
			{
				name: `disk-${form.disks.length + 1}`,
				type: 'dataVolume',
				size: '10Gi',
				storageClass: '',
				removed: false,
				isNew: true,
			},
		];
	}
</script>

{#snippet diskRow(disk: DiskRow)}
	{#if disk.isNew}
		<TextInput bind:value={disk.name} size="sm" width="w-24" />
		<TextInput bind:value={disk.size} size="sm" width="w-16" />
		<StorageClassSelect
			options={options?.storageClasses ?? []}
			bind:value={disk.storageClass}
			class="min-w-0 flex-1 py-1! text-xs"
		/>
	{:else}
		<span class="text-xs text-ink-muted"
			>{disk.name} ({disk.type}{disk.size ? ` · ${disk.size}` : ''}{disk.storageClass
				? ` · ${disk.storageClass}`
				: ''})</span
		>
	{/if}
{/snippet}
<RowListEditor
	items={form.disks}
	unit="disk(s)"
	addLabel="+ Add hard disk"
	rowLabel="Hard disk"
	empty="No disks."
	onadd={addDisk}
	ontoggle={(d) => (d.removed = !d.removed)}
	row={diskRow}
/>
