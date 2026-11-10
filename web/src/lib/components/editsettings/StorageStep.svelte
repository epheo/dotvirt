<script lang="ts">
	import { api, type Options, type StorageClassInfo, type VM } from '#lib/api.js';
	import { freeName } from '#lib/devices.js';
	import { diskError, diskErrors, type DiskRow, type EditForm } from '#lib/editform.js';
	import { quantityBytes } from '#lib/format.js';
	import { resource } from '#lib/resource.svelte.js';
	import { modules } from '#lib/state/modules.svelte.js';
	import { protectionCards } from '#lib/state/replication.svelte.js';
	import ProtectionPick from '../create/ProtectionPick.svelte';
	import RowListEditor from '../RowListEditor.svelte';
	import StorageClassSelect from '../StorageClassSelect.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		form = $bindable(),
		options,
		vm,
	}: { form: EditForm; options: Options | null; vm: VM } = $props();

	const errors = $derived(diskErrors(form));

	// The class facts the protection cards need, read only where r-drbd serves.
	const classesRes = resource<StorageClassInfo[] | null>(
		() => (modules.value?.replicatedStorage ? 'storage-classes' : ''),
		(k) => (k ? api.storageClasses() : Promise.resolve(null)),
	);
	const classes = $derived(classesRes.data ?? []);
	const cards = $derived(protectionCards(classes));
	const added = $derived(form.disks.filter((d) => d.isNew && !d.removed));
	const sizes = $derived(
		added.map((d) => ({ storageClass: d.storageClass ?? '', bytes: quantityBytes(d.size ?? '') })),
	);

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
		<TextInput bind:value={disk.size} size="sm" width="w-16" aria-label="New disk size" />
		{#if !cards}
			<StorageClassSelect
				options={options?.storageClasses ?? []}
				bind:value={disk.storageClass}
				class="min-w-0 flex-1 py-1! text-xs"
			/>
		{/if}
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
{#if cards}
	{#each added as disk, i (i)}
		<div class="mt-3">
			<ProtectionPick
				disk={disk.name}
				value={disk.storageClass ?? ''}
				onpick={(c) => (disk.storageClass = c)}
				{classes}
				bytes={quantityBytes(disk.size ?? '')}
				peers={sizes.filter((_, j) => j !== i)}
				{vm}
			/>
		</div>
	{/each}
{/if}
