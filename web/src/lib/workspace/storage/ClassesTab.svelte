<script lang="ts">
	import { type StorageClassInfo } from '#lib/api.js';
	import { hrefForScope } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { classTag, num, size, type DiskRow } from '#lib/storage.js';
	import type { Tone } from '#lib/status.js';
	import Card from '#lib/components/Card.svelte';
	import Note from '#lib/components/Note.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// Every class as one row: what the platform offers (free capacity, live
	// migration, snapshots) against what VM disks hold on it. A class disks
	// name that the cluster lacks still lists, flagged, since its disks can
	// never be provisioned.
	let { rows }: { rows: DiskRow[] } = $props();

	const classes = $derived(new Map((storage.classes ?? []).map((c) => [c.name, c])));
	const facts = $derived(classes.size > 0);

	// The tree's order: the default class, then local, then shared.
	const rank = (info?: StorageClassInfo) => (info?.default ? 0 : info?.profile?.shared ? 2 : 1);
	const list = $derived(
		[...storage.classNames]
			.sort((a, b) => rank(classes.get(a)) - rank(classes.get(b)))
			.map((name) => {
				const info = classes.get(name);
				const disks = rows.filter((r) => !r.ephemeral && r.storageClass === name);
				return {
					name,
					info,
					state: stateOf(info, disks.length > 0),
					vms: new Set(disks.map((d) => d.vmKey)).size,
					disks: disks.length,
					held: disks.reduce((n, d) => n + (Number.isNaN(d.size) ? 0 : d.size), 0),
				};
			}),
	);

	// A class disks reference but the cluster lacks leaves their DataVolumes
	// pending forever; an unrecognized CDI profile means DataVolumes need
	// explicit claim settings the create flow does not write.
	function stateOf(
		info: StorageClassInfo | undefined,
		used: boolean,
	): { tone: Tone; label: string; title: string } | null {
		if (!info)
			return facts && used
				? {
						tone: 'danger',
						label: 'Not on cluster',
						title: 'Disks on this class cannot be provisioned',
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

	const defaults = $derived((storage.classes ?? []).filter((c) => c.default).length);
	// Without KubeVirt the rows (VM disks) are empty, so their columns go, and
	// a claim here is a volume. An unknown state keeps VM terms.
	const vmTerms = $derived(!vmsOff());
	const things = $derived(vmTerms ? 'Disks' : 'Volumes');
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5 whitespace-nowrap';
</script>

{#if facts && defaults !== 1}
	<Note tone="warn" class="px-3 py-2 text-xs">
		{#if defaults === 0}
			No default storage class. {things} that name no class stay pending until the platform marks one
			as default.
		{:else}
			{defaults} storage classes are marked default. Kubernetes picks the newest, so {things.toLowerCase()}
			that name no class may not land where expected.
		{/if}
	</Note>
{/if}

<Card title="Storage classes" meta="Managed by the cluster platform, not dotvirt" flush>
	<div class="overflow-x-auto">
		<table class="w-full border-collapse text-[13px] tabular-nums">
			<thead>
				<tr>
					<th scope="col" class="{TH} pl-card">Class</th>
					<th scope="col" class={TH}>Kind</th>
					<th
						scope="col"
						class="{TH} text-right"
						title="Unallocated capacity the storage driver reports">Free</th
					>
					{#if vmTerms}
						<th scope="col" class="{TH} text-right" title="Capacity VM disks request on this class"
							>VM disks hold</th
						>
						<th scope="col" class="{TH} text-right">Disks</th>
						<th scope="col" class="{TH} text-right">VMs</th>
						<th scope="col" class={TH}>Live migration</th>
					{:else}
						<th
							scope="col"
							class={TH}
							title="Whether a volume here can attach to many hosts at once">Access</th
						>
					{/if}
					<th scope="col" class={TH}>Snapshots</th>
					<th scope="col" class="{TH} pr-card">Provisioner</th>
				</tr>
			</thead>
			<tbody>
				{#each list as c (c.name)}
					<tr class="hover:bg-select-soft">
						<td class="{TD} pl-card">
							<a
								href={hrefForScope({ kind: 'storage', storageClass: c.name })}
								class="font-mono text-xs font-medium text-accent-ink hover:underline">{c.name}</a
							>
							{#if c.state}
								<span class="ml-1.5"
									><StatusPill
										tone={c.state.tone}
										label={c.state.label}
										title={c.state.title}
									/></span
								>
							{/if}
						</td>
						<td class={TD}>{c.info ? classTag(c.info) || '-' : '-'}</td>
						<td class="{TD} text-right"
							>{c.info?.free != null ? size(c.info.free) : 'Not reported'}</td
						>
						{#if vmTerms}
							<td class="{TD} text-right">{c.disks ? size(c.held) : '-'}</td>
							<td class="{TD} text-right">{num(c.disks)}</td>
							<td class="{TD} text-right">{num(c.vms)}</td>
						{/if}
						<td class={TD}>
							{#if !c.info?.profile}
								<span class="text-ink-faint">-</span>
							{:else if c.info.profile.accessModes.includes('ReadWriteMany')}
								<span class="text-ok-ink">{vmTerms ? 'Possible' : 'Many hosts'}</span>
							{:else if vmTerms}
								<span
									class="text-warn-ink"
									title="Disks here attach to one host: their VMs cannot live-migrate"
									>Not possible</span
								>
							{:else}
								<span class="text-ink-soft">One host at a time</span>
							{/if}
						</td>
						<td class={TD}>
							{#if !c.info}<span class="text-ink-faint">-</span>
							{:else if c.info.snapshotClass}Yes
							{:else}<span
									class="text-ink-faint"
									title="No VolumeSnapshotClass for this provisioner">No</span
								>{/if}
						</td>
						<td class="{TD} pr-card font-mono text-xs text-ink-muted"
							>{c.info?.provisioner ?? '-'}</td
						>
					</tr>
				{:else}
					<tr>
						<td colspan={vmTerms ? 9 : 6} class="px-card py-6 text-center text-ink-faint">
							{storage.classesError ||
								(storage.classes === null && inventory.inventory
									? 'Reading storage classes.'
									: 'No storage classes visible.')}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</Card>
