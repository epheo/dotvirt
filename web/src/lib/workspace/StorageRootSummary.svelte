<script lang="ts">
	import type { VM } from '$lib/api';
	import { vmStorageKeys, DEFAULT_CLASS, NO_STORAGE } from '$lib/lenses';
	import { hrefForScope } from '$lib/nav';
	import { inventory } from '$lib/state/inventory.svelte';
	import InfoCard from '$lib/components/InfoCard.svelte';

	// The Storage root's Summary (Datastores analog): every storage class with
	// its VM/disk footprint. The compute cluster cards say nothing about storage,
	// so this root gets its own fact sheet instead of borrowing them.
	let { vms }: { vms: VM[] } = $props();

	const rows = $derived.by(() => {
		const catalog = inventory.options?.storageClasses ?? [];
		// Classes VMs actually use join the catalog's: an adopted VM may reference
		// a class the options catalog no longer lists.
		const names = new Set(catalog.map((c) => c.name));
		for (const vm of vms)
			for (const k of vmStorageKeys(vm, inventory.defaultStorageClass))
				if (k !== NO_STORAGE) names.add(k);
		return [...names].sort().map((name) => {
			const attached = vms.filter((vm) =>
				vmStorageKeys(vm, inventory.defaultStorageClass).includes(name),
			);
			const disks = attached.reduce(
				(n, vm) =>
					n +
					(vm.disks ?? []).filter(
						(d) =>
							d.type === 'dataVolume' &&
							(d.storageClass || inventory.defaultStorageClass || DEFAULT_CLASS) === name,
					).length,
				0,
			);
			return {
				name,
				isDefault: catalog.find((c) => c.name === name)?.default ?? false,
				vmCount: attached.length,
				disks,
			};
		});
	});
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		<InfoCard title="Storage classes">
			{#if rows.length}
				<ul class="divide-y divide-line-soft px-3 text-[13px]">
					{#each rows as r (r.name)}
						<li class="flex items-center justify-between gap-3 py-1.5">
							<span class="flex items-center gap-2">
								<a
									href={hrefForScope({ kind: 'storage', storageClass: r.name })}
									class="text-accent hover:underline">{r.name}</a
								>
								{#if r.isDefault}
									<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
										>default</span
									>
								{/if}
							</span>
							<span class="text-ink-faint">{r.vmCount} VMs · {r.disks} disks</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="px-3 py-3 text-xs text-ink-faint">No storage classes visible.</p>
			{/if}
			<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-faint">
				Storage classes are managed by the cluster platform, not dotvirt.
			</p>
		</InfoCard>
	</div>
</div>
