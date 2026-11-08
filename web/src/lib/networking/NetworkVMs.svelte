<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { vmKey, vmRef, type VM } from '#lib/api.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Inspector from '#lib/components/Inspector.svelte';
	import VMTable from '#lib/components/VMTable.svelte';

	// A VMs tab: the shared VM grid with the side peek (?peek=ns/name, resolved
	// against this list so a VM leaving it closes its peek).
	let { vms }: { vms: VM[] } = $props();

	const peekKey = $derived(page.url.searchParams.get('peek'));
	const peekVM = $derived(peekKey ? (vms.find((v) => vmKey(v) === peekKey) ?? null) : null);
	function setPeek(vm: VM | null) {
		const u = new URL(page.url.href);
		if (vm) u.searchParams.set('peek', vmKey(vm));
		else u.searchParams.delete('peek');
		goto(u.pathname + u.search, { replace: true, reset: false });
	}
</script>

<div class="mt-4 flex min-h-0 flex-1 border-t border-line bg-panel">
	<div class="min-w-0 flex-1">
		<VMTable
			{vms}
			staged={drafts.stagedByKey}
			activeKey={peekVM ? peekKey : null}
			onselect={(vm) => setPeek(peekKey === vmKey(vm) ? null : vm)}
			onstagedopen={(vm) => (ui.modal = { kind: 'staged', vm: vmRef(vm) })}
			oncontextvm={(vm, x, y) => ui.openVMContext(vm, x, y)}
		/>
	</div>
	{#if peekVM}
		<Inspector vm={peekVM} onclose={() => setPeek(null)} />
	{/if}
</div>
