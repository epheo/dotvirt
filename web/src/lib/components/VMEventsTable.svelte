<script lang="ts">
	import { untrack } from 'svelte';
	import { api, type VM } from '$lib/api';
	import EventsTable from './EventsTable.svelte';

	// The Monitor tab's Kubernetes-events lane. Owns its load: it is mounted
	// only while the lane is visible, so the mount-time fetch IS the lazy load.
	let { vm }: { vm: VM } = $props();

	let events = $state<Awaited<ReturnType<typeof api.events>> | null>(null);
	let loading = $state(false);

	function load() {
		loading = true;
		api
			.events(vm.namespace, vm.name)
			.then((e) => (events = e))
			.catch(() => (events = [])) // a 401 signs out centrally via the api layer
			.finally(() => (loading = false));
	}

	// Reload on selection change only — the stream hands down a fresh vm object
	// every frame, and load() reads vm.namespace/name synchronously.
	const vmKey = $derived(`${vm.namespace}/${vm.name}`);
	$effect(() => {
		vmKey;
		untrack(() => {
			events = null;
			load();
		});
	});
</script>

<EventsTable {events} {loading} />
