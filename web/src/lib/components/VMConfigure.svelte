<script lang="ts">
	import { Pencil } from 'lucide-svelte';
	import type { Network, VM } from '$lib/api';
	import { manifestURL } from '$lib/actions';
	import { resolveNIC, kindLabel } from '$lib/networks';
	import InfoCard from './InfoCard.svelte';
	import Row from './Row.svelte';

	// vCenter's settings verb: a left sub-rail of read-only sections; every
	// Edit stages a change through the PR flow (nothing writes the cluster).
	let {
		vm,
		networks = [],
		onedit,
		onsearchlabel,
	}: {
		vm: VM;
		// The port-group catalog, to resolve raw NIC refs into port groups.
		networks?: Network[];
		// Opens the edit modal jumped to the given section.
		onedit: (section: 'compute' | 'scheduling' | 'storage' | 'network' | 'labels') => void;
		onsearchlabel?: (key: string, value: string) => void;
	} = $props();

	type Section = 'hardware' | 'scheduling' | 'storage' | 'network' | 'labels' | 'source';
	let view = $state<Section>('hardware');

	const groupDesc = (g: { mode: string; strict?: boolean }) =>
		`${g.mode === 'apart' ? 'keep apart' : 'keep together'}${g.strict ? ', strict' : ', preferred'}`;
</script>

{#snippet editButton(section: 'compute' | 'scheduling' | 'storage' | 'network' | 'labels')}
	<button
		onclick={() => onedit(section)}
		class="flex items-center gap-1 text-xs text-accent hover:underline"
	>
		<Pencil size={11} /> Edit
	</button>
{/snippet}

<div class="flex gap-4">
	<nav class="w-36 shrink-0 text-[13px]">
		{#each [['hardware', 'VM Hardware'], ['scheduling', 'Scheduling'], ['storage', 'Storage'], ['network', 'Network'], ['labels', 'Labels'], ['source', 'Source & sync']] as const as [id, label] (id)}
			<button
				onclick={() => (view = id)}
				class="block w-full rounded px-2.5 py-1.5 text-left {view === id
					? 'bg-select-soft font-medium text-accent-ink'
					: 'text-ink-soft hover:bg-inset'}"
			>
				{label}
			</button>
		{/each}
	</nav>
	<div class="min-w-0 flex-1">
		{#if view === 'hardware'}
			<InfoCard title="VM Hardware">
				{#snippet action()}{@render editButton('compute')}{/snippet}
				<dl class="divide-y divide-line-soft text-[13px]">
					<Row label="CPU cores" value={vm.cpuCores ? String(vm.cpuCores) : ''} />
					<Row label="Memory" value={vm.memory ?? ''} />
					<Row label="Instance type" value={vm.instancetype ?? ''} />
					<Row label="Preference" value={vm.preference ?? ''} />
					<Row label="Power (desired)" value={vm.power} />
				</dl>
			</InfoCard>
		{:else if view === 'scheduling'}
			<InfoCard title="Scheduling">
				{#snippet action()}{@render editButton('scheduling')}{/snippet}
				<dl class="divide-y divide-line-soft text-[13px]">
					{#if vm.scheduling?.custom}
						<Row label="Placement" value="Hand-written affinity — managed in git" />
					{:else}
						{#each vm.scheduling?.groups ?? [] as g (g.name)}
							<Row label={`Group ${g.name}`} value={groupDesc(g)} />
						{/each}
						<Row
							label="Host pinning"
							value={vm.scheduling?.pin?.length ? vm.scheduling.pin.join(', ') : 'Any host'}
						/>
					{/if}
					<Row
						label="DRS"
						value={vm.drsExclude ? 'Excluded from load balancing' : 'Eligible for load balancing'}
					/>
					<Row label="Eviction strategy" value={vm.evictionStrategy || 'Cluster default'} />
				</dl>
			</InfoCard>
		{:else if view === 'storage'}
			<InfoCard title="Disks">
				{#snippet action()}{@render editButton('storage')}{/snippet}
				{#if vm.disks?.length}
					<ul class="divide-y divide-line-soft px-3 text-[13px]">
						{#each vm.disks as d (d.name)}
							<li class="flex justify-between gap-3 py-1.5">
								<span class="text-ink">{d.name}</span>
								<span class="text-ink-faint"
									>{d.type}{d.size ? ` · ${d.size}` : ''}{d.storageClass
										? ` · ${d.storageClass}`
										: ''}</span
								>
							</li>
						{/each}
					</ul>
				{:else}
					<p class="px-3 py-3 text-xs text-ink-faint">No disks defined in the manifest.</p>
				{/if}
			</InfoCard>
		{:else if view === 'network'}
			<InfoCard title="Network adapters">
				{#snippet action()}{@render editButton('network')}{/snippet}
				{#if vm.networks?.length}
					<ul class="divide-y divide-line-soft px-3 text-[13px]">
						{#each vm.networks as n (n.name)}
							{@const pg = resolveNIC(n, vm.namespace, networks)}
							{@const detail = [
								n.ip || null,
								n.mac || null,
								pg?.scope === 'shared' ? 'shared' : null,
								pg?.uplink ? `uplink ${pg.uplink}` : null,
								pg?.subnets?.length ? pg.subnets.join(', ') : null,
							]
								.filter(Boolean)
								.join(' · ')}
							<li class="py-1.5">
								<div class="flex items-baseline justify-between gap-3">
									<span class="text-ink">{n.name}</span>
									<span class="flex items-center gap-2 text-right">
										<span class="text-ink-soft"
											>{pg
												? pg.name
												: n.network && n.network !== 'pod'
													? n.network
													: 'Pod network'}</span
										>
										{#if pg}
											<span
												class="shrink-0 rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
												>{kindLabel(pg.kind)}{pg.vlan ? ` ${pg.vlan}` : ''}</span
											>
										{/if}
									</span>
								</div>
								{#if detail}
									<div class="mt-0.5 text-right text-[11px] text-ink-faint">{detail}</div>
								{/if}
							</li>
						{/each}
					</ul>
				{:else}
					<p class="px-3 py-3 text-xs text-ink-faint">No adapters defined in the manifest.</p>
				{/if}
			</InfoCard>
		{:else if view === 'labels'}
			<InfoCard title="Labels">
				{#snippet action()}{@render editButton('labels')}{/snippet}
				<div class="px-3 py-2">
					{#if vm.labels && Object.keys(vm.labels).length}
						{#each Object.entries(vm.labels) as [k, v] (k)}
							<button
								onclick={() => onsearchlabel?.(k, v)}
								title="Find everything labeled {k}={v}"
								class="mr-1 mb-1 inline-block rounded bg-inset-strong px-1.5 py-0.5 text-xs text-ink-soft hover:bg-select-soft hover:text-accent-ink"
								>{k}={v}</button
							>
						{/each}
					{:else}
						<p class="py-1 text-xs text-ink-faint">No labels.</p>
					{/if}
				</div>
			</InfoCard>
		{:else}
			<InfoCard title="Source & sync">
				<dl class="divide-y divide-line-soft text-[13px]">
					<Row label="Manifest" value={vm.sourceFile} mono />
					<Row label="Namespace" value={vm.namespace} />
					<Row label="Sync" value={vm.sync} />
				</dl>
				<div class="border-t border-line-soft px-3 py-2">
					<a href={manifestURL(vm)} target="_blank" class="text-xs text-accent hover:underline"
						>Download manifest ↗</a
					>
					<p class="mt-1 text-xs text-ink-faint">
						This VM's configuration lives in git; edits become a pull request.
					</p>
				</div>
			</InfoCard>
		{/if}
	</div>
</div>
