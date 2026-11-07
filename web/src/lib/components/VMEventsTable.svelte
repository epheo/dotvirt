<script lang="ts">
	import { api, vmKey, type VM } from '#lib/api.js';
	import { resource } from '#lib/resource.svelte.js';
	import EventsTable from './EventsTable.svelte';

	// The Monitor tab's Kubernetes-events lane. Owns its load: it is mounted
	// only while the lane is visible, so the mount-time fetch IS the lazy load.
	let { vm }: { vm: VM } = $props();

	// Keyed on identity (the stream hands down a fresh vm object every frame).
	const key = $derived(vmKey(vm));
	const evRes = resource(
		() => key,
		() => api.events(vm.namespace, vm.name),
		{ reset: true },
	);
</script>

<EventsTable
	events={evRes.data}
	loading={evRes.loading}
	error={evRes.error}
	onretry={evRes.refresh}
/>
