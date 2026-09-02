<script lang="ts">
	import { page } from '$app/state';
	import { Pencil, Trash2 } from 'lucide-svelte';
	import type { VM } from '$lib/api';
	import { vmHref } from '$lib/nav';
	import { drafts } from '$lib/state/drafts.svelte';
	import { TONE_PILL } from '$lib/status';
	import { ui } from '$lib/state/ui.svelte';
	import PowerDot from '$lib/components/PowerDot.svelte';
	import SyncBadge from '$lib/components/SyncBadge.svelte';
	import TreeRow from '$lib/components/TreeRow.svelte';

	// The VM leaf every section tree renders: power dot, name (struck through
	// when a delete is staged), and the staged badge or sync state.
	let { vm, indent = 2 }: { vm: VM; indent?: 2 | 3 } = $props();

	const key = $derived(`${vm.namespace}/${vm.name}`);
	const sc = $derived(drafts.stagedByKey.get(key));
	const active = $derived.by(() => {
		const parts = page.url.pathname.split('/');
		return (
			parts[1] === 'vm' &&
			parts.length >= 4 &&
			`${decodeURIComponent(parts[2])}/${decodeURIComponent(parts[3])}` === key
		);
	});

	function oncontextmenu(e: MouseEvent) {
		e.preventDefault();
		ui.openVMContext(vm, e.clientX, e.clientY);
	}
</script>

<TreeRow {indent} {active} href={vmHref(vm.namespace, vm.name)} {oncontextmenu}>
	{#snippet icon()}
		<PowerDot power={vm.power} paused={vm.paused} />
	{/snippet}
	<span class="truncate {sc?.kind === 'delete' ? 'text-side-dim line-through' : 'text-side-ink'}"
		>{vm.name}</span
	>
	{#snippet trailing()}
		{#if sc}
			<!-- Not draftKindTone: a staged create (adoption) reads info here, not ok. -->
			<span
				class="inline-flex items-center rounded px-1 text-[10px] font-medium {TONE_PILL[
					sc.kind === 'delete' ? 'danger' : 'info'
				]}"
				title="Staged {sc.kind}"
			>
				{#if sc.kind === 'delete'}<Trash2 size={10} />{:else}<Pencil size={10} />{/if}
			</span>
		{:else}
			<SyncBadge sync={vm.sync} error={vm.syncError} compact />
		{/if}
	{/snippet}
</TreeRow>
