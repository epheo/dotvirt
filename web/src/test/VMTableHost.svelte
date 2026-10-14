<script lang="ts">
	import type { DraftItem, VM } from '$lib/api';
	import VMTable from '$lib/components/VMTable.svelte';

	// Binds selected the way the workspace does, and shows it, so a test sees
	// the selection the bulk bar would act on.
	let { vms }: { vms: VM[] } = $props();
	let selected = $state(new Set<string>());
</script>

<VMTable
	{vms}
	bind:selected
	staged={new Map<string, DraftItem>()}
	onselect={() => {}}
	onstagedopen={() => {}}
/>
<output>{[...selected].sort().join(',')}</output>
