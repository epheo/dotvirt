<script lang="ts">
	import type { VM } from '$lib/api';
	import { DEFAULT_CLASS } from '$lib/lenses';
	import { vmHref } from '$lib/nav';
	import InfoCard from '$lib/components/InfoCard.svelte';
	import PowerDot from '$lib/components/PowerDot.svelte';
	import Row from '$lib/components/Row.svelte';

	// The storage-class object page's Summary (Datastore analog): the class and
	// the VMs with provisioned disks on it.
	let { storageClass, vms }: { storageClass: string; vms: VM[] } = $props();

	const diskCount = $derived(
		vms.reduce(
			(n, vm) =>
				n +
				(vm.disks ?? []).filter(
					(d) => d.type === 'dataVolume' && (d.storageClass || DEFAULT_CLASS) === storageClass
				).length,
			0
		)
	);
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		<InfoCard title="Storage class: {storageClass}">
			<dl class="divide-y divide-slate-100 text-[13px]">
				<Row label="VMs attached" value={String(vms.length)} />
				<Row label="Provisioned disks" value={String(diskCount)} />
			</dl>
			<p class="border-t border-slate-100 px-3 py-2 text-xs text-ink-faint">
				Storage classes are managed by the cluster platform, not dotvirt.
			</p>
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
