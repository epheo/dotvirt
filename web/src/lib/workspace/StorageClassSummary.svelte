<script lang="ts">
	import { api, type StorageClassInfo, type VM } from '$lib/api';
	import { bytes, relativeAge } from '$lib/format';
	import { disksOnClass, provisionedBytes } from '$lib/lenses';
	import { vmHref } from '$lib/nav';
	import { resource } from '$lib/resource.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import InfoCard from '$lib/components/InfoCard.svelte';
	import Note from '$lib/components/Note.svelte';
	import PowerDot from '$lib/components/PowerDot.svelte';
	import Row from '$lib/components/Row.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';

	// The storage-class object page's Summary: the platform's facts about the
	// class (what a disk on it gets, what the driver has left), then the disks
	// and VMs provisioned on it.
	let { storageClass, vms }: { storageClass: string; vms: VM[] } = $props();

	const classesRes = resource<StorageClassInfo[]>(() => 'storage-classes', api.storageClasses, {
		poll: 30000,
	});
	const info = $derived(
		classesRes.failed ? undefined : classesRes.data?.find((c) => c.name === storageClass),
	);
	// The class list loaded and this name is not in it: an adopted or hand-
	// edited VM points at a class the cluster lacks.
	const missing = $derived(!classesRes.failed && !!classesRes.data && !info);

	const disks = $derived(
		disksOnClass(vms, storageClass, inventory.defaultStorageClass).sort(
			(a, b) => a.vm.name.localeCompare(b.vm.name) || a.disk.name.localeCompare(b.disk.name),
		),
	);
	const provisioned = $derived(provisionedBytes(disks));
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		{#if missing}
			<Note tone="danger" class="px-3 py-2 text-xs">
				This storage class does not exist on the cluster. Disks on it cannot be provisioned; move
				them to an existing class.
			</Note>
		{:else if info?.profile && !info.profile.recognized}
			<Note tone="warn" class="px-3 py-2 text-xs">
				CDI does not recognize this provisioner, so DataVolumes need explicit access and volume
				modes.
			</Note>
		{/if}

		<InfoCard title="Storage class: {storageClass}">
			<dl class="divide-y divide-line-soft text-[13px]">
				{#if info}
					{#if info.description}
						<Row label="Description" value={info.description} />
					{/if}
					<Row label="Default class" value={info.default ? 'Yes' : 'No'} />
					<Row label="Provisioner" value={info.provisioner} mono />
					<Row label="Reclaim policy" value={info.reclaimPolicy} />
					<Row label="Volume binding" value={info.bindingMode} />
					<Row label="Disk resize" value={info.expandable ? 'Allowed' : 'Not allowed'} />
					{#if info.profile}
						<Row label="Access modes" value={info.profile.accessModes.join(', ')} />
						<Row label="Volume mode" value={info.profile.volumeMode} />
						<Row label="Live migration">
							{#if info.profile.shared}
								<StatusPill tone="ok" label="Supported" />
							{:else}
								<StatusPill
									tone="warn"
									label="RWO only"
									title="Disks here are single-attach: VMs using them cannot live-migrate"
								/>
							{/if}
						</Row>
						<Row label="Clone strategy" value={info.profile.cloneStrategy} />
					{/if}
					<Row label="Snapshots" value={info.snapshotClass || 'Not available'} />
					{#if info.free != null}
						<Row label="Free capacity" value={bytes(info.free)} />
						{#if (info.segments ?? []).length > 1}
							{#each info.segments ?? [] as seg (seg.topology)}
								<Row label="Free on {seg.topology || 'cluster'}" value={bytes(seg.free)} />
							{/each}
						{/if}
					{/if}
				{/if}
				<Row label="Provisioned by VMs" value={disks.length ? bytes(provisioned) : '—'} />
				<Row label="Provisioned disks" value={String(disks.length)} />
				<Row label="VMs attached" value={String(vms.length)} />
				{#if info?.created}
					<Row label="Created" value={relativeAge(info.created)} />
				{/if}
			</dl>
			<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-faint">
				Storage classes are managed by the cluster platform, not dotvirt.
			</p>
		</InfoCard>

		<!-- A VM is attached exactly when it has a disk here, so the disk table is
		     also the VM list: one row per disk, the VM linked on each. -->
		{#if disks.length}
			<InfoCard title="Disks">
				<table class="w-full text-left text-[13px]">
					<thead class="border-b border-line-soft text-xs text-ink-muted">
						<tr>
							<th class="px-3 py-1.5 font-medium">VM</th>
							<th class="px-3 py-1.5 font-medium">Disk</th>
							<th class="px-3 py-1.5 text-right font-medium">Size</th>
						</tr>
					</thead>
					<tbody>
						{#each disks as d (d.vm.namespace + '/' + d.vm.name + '/' + d.disk.name)}
							<tr class="border-b border-line-soft last:border-0">
								<td class="px-3 py-1.5 whitespace-nowrap">
									<a
										href={vmHref(d.vm.namespace, d.vm.name)}
										class="inline-flex items-center gap-1 text-ink hover:text-accent-ink"
									>
										<PowerDot power={d.vm.power} paused={d.vm.paused} />{d.vm.name}
									</a>
									<span class="text-xs text-ink-faint">{d.vm.namespace}</span>
								</td>
								<td class="px-3 py-1.5 font-mono text-xs text-ink-soft">{d.disk.name}</td>
								<td class="px-3 py-1.5 text-right whitespace-nowrap text-ink-soft">
									{d.disk.size || '—'}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</InfoCard>
		{/if}
	</div>
</div>
