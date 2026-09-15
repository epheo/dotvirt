<script lang="ts">
	import { api, vmKey, type VM } from '$lib/api';
	import { resource } from '$lib/resource.svelte';
	import EventsTable from './EventsTable.svelte';

	// The Monitor tab's Kubernetes-events lane. Owns its load: it is mounted
	// only while the lane is visible, so the mount-time fetch IS the lazy load.
	let { vm }: { vm: VM } = $props();

	// Keyed on identity (the stream hands down a fresh vm object every frame);
	// a failed read renders as an empty list.
	const key = $derived(vmKey(vm));
	const evRes = resource(
		() => key,
		() => api.events(vm.namespace, vm.name),
		{ reset: true },
	);
	const events = $derived(evRes.failed ? [] : evRes.data);
	const loading = $derived(evRes.loading);
</script>

<EventsTable {events} {loading} />
