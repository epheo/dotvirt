<script lang="ts">
	import { api, type StorageClassInfo, type VM } from '$lib/api';
	import { bytes } from '$lib/format';
	import { disksOnClass, provisionedBytes, vmStorageKeys, NO_STORAGE } from '$lib/lenses';
	import { hrefForScope } from '$lib/nav';
	import { resource } from '$lib/resource.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import type { Tone } from '$lib/status';
	import Note from '$lib/components/Note.svelte';
	import StatusPill from '$lib/components/StatusPill.svelte';

	// The Storage root's Summary: every class as one row - what the platform
	// offers (provisioner, free capacity, live-migration and snapshot support)
	// against what VMs hold on it. The fact-sheet feed absents its columns
	// while unavailable; VM footprint alone still yields a row per class, like
	// the tree.
	let { vms }: { vms: VM[] } = $props();

	const classesRes = resource<StorageClassInfo[]>(() => 'storage-classes', api.storageClasses, {
		poll: 30000,
	});
	const classes = $derived(
		new Map((classesRes.failed ? [] : (classesRes.data ?? [])).map((c) => [c.name, c])),
	);
	const hasFacts = $derived(classes.size > 0);
	const hasFree = $derived([...classes.values()].some((c) => c.free != null));

	const rows = $derived.by(() => {
		const catalog = inventory.options?.storageClasses ?? [];
		// Classes VMs actually use join the cluster's: an adopted VM may reference
		// a class the cluster no longer has, and that is exactly what to surface.
		const names = new Set([...classes.keys(), ...catalog.map((c) => c.name)]);
		for (const vm of vms)
			for (const k of vmStorageKeys(vm, inventory.defaultStorageClass))
				if (k !== NO_STORAGE) names.add(k);
		return [...names].sort().map((name) => {
			const info = classes.get(name);
			const disks = disksOnClass(vms, name, inventory.defaultStorageClass);
			const attached = new Set(disks.map((d) => d.vm.namespace + '/' + d.vm.name));
			return {
				name,
				info,
				isDefault: info?.default ?? catalog.find((c) => c.name === name)?.default ?? false,
				state: stateOf(info, hasFacts, attached.size > 0),
				vmCount: attached.size,
				diskCount: disks.length,
				provisioned: provisionedBytes(disks),
			};
		});
	});

	// A class VMs reference but the cluster lacks leaves their DataVolumes
	// pending forever; an unrecognized CDI profile means DataVolumes need
	// explicit claim settings the wizard does not write.
	function stateOf(
		info: StorageClassInfo | undefined,
		facts: boolean,
		used: boolean,
	): { tone: Tone; label: string; title: string } | null {
		if (!info)
			return facts && used
				? {
						tone: 'danger',
						label: 'Not on cluster',
						title: 'VM disks on this class cannot be provisioned',
					}
				: null;
		if (info.profile && !info.profile.recognized)
			return {
				tone: 'warn',
				label: 'Unrecognized',
				title:
					'CDI does not know this provisioner: DataVolumes need explicit access and volume modes',
			};
		return null;
	}

	const defaults = $derived(rows.filter((r) => r.isDefault).length);
	const segmentsTitle = (info: StorageClassInfo) =>
		(info.segments ?? []).length > 1
			? (info.segments ?? []).map((s) => `${s.topology || 'cluster'}: ${bytes(s.free)}`).join('\n')
			: '';
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex (axe scrollable-region-focusable: a scroll region must be keyboard-reachable) -->
<div class="min-h-0 flex-1 overflow-y-auto" role="region" aria-label="Storage classes" tabindex="0">
	{#if hasFacts && defaults !== 1}
		<Note tone="warn" class="mx-4 mt-4 px-3 py-2 text-xs">
			{#if defaults === 0}
				No default storage class. Disks that name no class stay pending until the platform marks one
				as default.
			{:else}
				{defaults} storage classes are marked default. Kubernetes picks the newest, so disks that name
				no class may not land where expected.
			{/if}
		</Note>
	{/if}
	{#if rows.length}
		<table class="w-full text-left text-[13px]">
			<thead class="border-b border-line text-xs text-ink-muted">
				<tr>
					<th class="w-0 px-4 py-2 font-medium whitespace-nowrap">Class</th>
					<th class="w-0 px-4 py-2 font-medium whitespace-nowrap">State</th>
					{#if hasFacts}
						<th class="w-0 px-4 py-2 font-medium whitespace-nowrap">Provisioner</th>
					{/if}
					{#if hasFree}
						<th
							class="w-0 px-4 py-2 text-right font-medium whitespace-nowrap"
							title="Unallocated capacity the storage driver reports"
						>
							Free
						</th>
					{/if}
					<th
						class="w-0 px-4 py-2 text-right font-medium whitespace-nowrap"
						title="Capacity requested by VM disks on this class"
					>
						Provisioned
					</th>
					<th class="w-0 px-4 py-2 text-right font-medium whitespace-nowrap">VMs</th>
					<th class="w-0 px-4 py-2 text-right font-medium whitespace-nowrap">Disks</th>
					{#if hasFacts}
						<th
							class="w-0 px-4 py-2 font-medium whitespace-nowrap"
							title="A VM can only live-migrate when its disks allow shared (ReadWriteMany) access"
						>
							Live migration
						</th>
						<th class="px-4 py-2 font-medium whitespace-nowrap">Snapshots</th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each rows as r (r.name)}
					<tr class="border-b border-line-soft hover:bg-select-soft">
						<td class="px-4 py-1.5 whitespace-nowrap">
							<a
								href={hrefForScope({ kind: 'storage', storageClass: r.name })}
								class="font-medium text-ink hover:text-accent-ink">{r.name}</a
							>
							{#if r.isDefault}
								<span class="ml-1 rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
									>default</span
								>
							{/if}
						</td>
						<td class="px-4 py-1.5 whitespace-nowrap">
							{#if r.state}
								<StatusPill tone={r.state.tone} label={r.state.label} title={r.state.title} />
							{:else if r.info}
								<StatusPill tone="ok" label="Available" />
							{:else}
								<span class="text-ink-faint">—</span>
							{/if}
						</td>
						{#if hasFacts}
							<td class="px-4 py-1.5 font-mono text-xs whitespace-nowrap text-ink-soft"
								>{r.info?.provisioner ?? '—'}</td
							>
						{/if}
						{#if hasFree}
							<td
								class="px-4 py-1.5 text-right whitespace-nowrap text-ink-soft"
								title={r.info ? segmentsTitle(r.info) : ''}
							>
								{r.info?.free != null ? bytes(r.info.free) : '—'}
							</td>
						{/if}
						<td class="px-4 py-1.5 text-right whitespace-nowrap text-ink-soft">
							{r.diskCount ? bytes(r.provisioned) : '—'}
						</td>
						<td class="px-4 py-1.5 text-right whitespace-nowrap text-ink-soft">{r.vmCount}</td>
						<td class="px-4 py-1.5 text-right whitespace-nowrap text-ink-soft">{r.diskCount}</td>
						{#if hasFacts}
							<td class="px-4 py-1.5 whitespace-nowrap">
								{#if r.info?.profile}
									{#if r.info.profile.shared}
										<StatusPill tone="ok" label="Supported" />
									{:else}
										<StatusPill
											tone="warn"
											label="RWO only"
											title="Disks here are single-attach: VMs using them cannot live-migrate"
										/>
									{/if}
								{:else}
									<span class="text-ink-faint">—</span>
								{/if}
							</td>
							<td class="px-4 py-1.5 whitespace-nowrap">
								{#if !r.info}
									<span class="text-ink-faint">—</span>
								{:else if r.info.snapshotClass}
									<span class="text-ok-ink">Yes</span>
								{:else}
									<span class="text-ink-faint" title="No VolumeSnapshotClass for this provisioner"
										>No</span
									>
								{/if}
							</td>
						{/if}
					</tr>
				{/each}
			</tbody>
		</table>
	{:else if classesRes.loading}
		<p class="px-4 py-3 text-xs text-ink-faint">Loading storage classes…</p>
	{:else}
		<p class="px-4 py-3 text-xs text-ink-faint">No storage classes visible.</p>
	{/if}
	<p class="px-4 py-2 text-xs text-ink-faint">
		Storage classes are managed by the cluster platform, not dotvirt.
	</p>
</div>
