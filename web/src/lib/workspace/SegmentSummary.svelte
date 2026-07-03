<script lang="ts">
	import type { VM } from '$lib/api';
	import { POD_NETWORK } from '$lib/lenses';
	import { networkByRef, kindLabel } from '$lib/networks';
	import { segmentType } from '$lib/vocab';
	import { vmHref } from '$lib/nav';
	import { inventory } from '$lib/state/inventory.svelte';
	import InfoCard from '$lib/components/InfoCard.svelte';
	import PowerDot from '$lib/components/PowerDot.svelte';
	import Row from '$lib/components/Row.svelte';

	// The segment object page's Summary: the port group's facts (from the
	// networking read layer) plus the VMs attached to it.
	let { network, vms }: { network: string; vms: VM[] } = $props();

	const pg = $derived(networkByRef(network, inventory.networks));
	const st = $derived(pg ? segmentType(pg) : null);
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		<InfoCard title={pg ? pg.name : network}>
			<dl class="divide-y divide-slate-100 text-[13px]">
				<Row
					label="Type"
					value={pg
						? `${kindLabel(pg.kind)}${st ? ` — ${st.nsx} · ${st.vsphere}` : ''}`
						: network === POD_NETWORK
							? 'Pod network (cluster default)'
							: ''}
				/>
				{#if pg}
					<Row
						label="Scope"
						value={pg.scope === 'shared' ? 'Shared · all projects' : `Project · ${pg.namespace}`}
					/>
					{#if pg.vlan}<Row label="VLAN" value={String(pg.vlan)} />{/if}
					{#if pg.uplink}<Row label="Uplink" value={pg.uplink} />{/if}
					{#if pg.subnets?.length}<Row label="Subnets" value={pg.subnets.join(', ')} />{/if}
					<Row label="Backing">
						<span class="text-ink-muted">{pg.backing}</span>
					</Row>
				{/if}
				<Row label="VMs attached" value={String(vms.length)} />
			</dl>
		</InfoCard>

		{#if vms.length}
			<InfoCard title="Attached VMs">
				<div class="flex flex-wrap gap-1 px-3 py-2">
					{#each vms as vm (vm.namespace + '/' + vm.name)}
						<a
							href={vmHref(vm.namespace, vm.name)}
							class="inline-flex items-center gap-1 rounded bg-inset px-1.5 py-0.5 text-[11px] text-slate-600 hover:bg-select-soft"
						>
							<PowerDot power={vm.power} paused={vm.paused} />{vm.name}
						</a>
					{/each}
				</div>
			</InfoCard>
		{/if}
	</div>
</div>
