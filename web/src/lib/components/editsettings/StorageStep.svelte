<script lang="ts">
	import type { Options } from '#lib/api.js';
	import { freeName } from '#lib/devices.js';
	import { diskError, diskErrors, type DiskRow, type EditForm } from '#lib/editform.js';
	import RowListEditor from '../RowListEditor.svelte';
	import StorageClassSelect from '../StorageClassSelect.svelte';
	import TextInput from '../TextInput.svelte';

	let { form = $bindable(), options }: { form: EditForm; options: Options | null } = $props();

	const errors = $derived(diskErrors(form));

	function addDisk() {
		form.disks = [
			...form.disks,
			{
				name: freeName(
					'disk-',
					form.disks.map((d) => d.name),
				),
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
	{@const err = diskError(form, disk)}
	{#if disk.isNew}
		<TextInput
			bind:value={disk.name}
			size="sm"
			width="w-24"
			aria-label="New disk name"
			aria-invalid={!!err}
			title={err}
		/>
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
{#each errors as e (e)}<p class="mt-1 text-xs text-danger-ink">{e}</p>{/each}
