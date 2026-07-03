<script lang="ts">
	import { goto } from '$app/navigation';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import NetworkTopology from '$lib/components/NetworkTopology.svelte';
	import { hrefForScope } from '$lib/nav';
	import { inventory } from '$lib/state/inventory.svelte';
</script>

<Breadcrumb trail={[{ label: 'Networking' }, { label: 'Topology' }]} />
<div class="min-h-0 flex-1 overflow-y-auto">
	<NetworkTopology
		networks={inventory.networks}
		uplinks={inventory.uplinks}
		vms={inventory.allVMs}
		projects={inventory.inventory?.projects ?? []}
		onpick={(net) => goto(hrefForScope({ kind: 'network', network: net }))}
	/>
</div>
