<script lang="ts">
	import { untrack } from 'svelte';
	import { clusterGate } from '#lib/gates.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { replication } from '#lib/state/replication.svelte.js';
	import type { DiskRow } from '#lib/storage.js';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Note from '#lib/components/Note.svelte';
	import ProtectionList from './ProtectionList.svelte';
	import ReplicationMap from './ReplicationMap.svelte';

	// One tab, two audiences, on the capability the server asks: an operator
	// sees where every copy lives, anyone else how safe their own disks are.
	let {
		rows,
		project,
		go,
	}: {
		rows: DiskRow[];
		project: string;
		go: (params: Record<string, string | undefined>, opts?: { replace?: boolean }) => void;
	} = $props();

	const gate = $derived(clusterGate.replication());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);

	$effect(() => {
		void inventory.replicationVersion;
		if (deciding) return;
		const operator = gate.ok;
		untrack(() => replication.pull(operator));
	});
</script>

<ErrorNote error={replication.error} />
{#if deciding}
	<p class="text-sm text-ink-muted">{CHECKING}</p>
{:else if gate.ok}
	{#if replication.overview?.stale}
		<Note tone="warn" class="px-3 py-2 text-xs">
			Showing the last known state: the watch on replicated storage is down.
		</Note>
	{/if}
	{#if !replication.overview?.synced}
		<p class="text-sm text-ink-muted">Reading replicated storage...</p>
	{:else}
		<ReplicationMap overview={replication.overview} {rows} {project} {go} />
	{/if}
{:else if replication.protection}
	<ProtectionList protection={replication.protection} {rows} {project} />
{:else if !replication.error}
	<p class="text-sm text-ink-muted">Reading your disks...</p>
{/if}
