<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import VMDetail from '$lib/components/VMDetail.svelte';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';

	const namespace = $derived(page.params.namespace!);
	const name = $derived(page.params.name!);
	// Derived from the live inventory by identity — every WS frame re-resolves it,
	// and a VM deleted mid-view degrades to the empty state instead of crashing.
	const vm = $derived(inventory.findVM(namespace, name));

	type Tab = 'summary' | 'monitor' | 'configure' | 'permissions' | 'snapshots' | 'console';
	const TABS: Tab[] = ['summary', 'monitor', 'configure', 'permissions', 'snapshots', 'console'];
	const tab = $derived.by<Tab>(() => {
		const t = page.url.searchParams.get('tab') as Tab | null;
		return t && TABS.includes(t) ? t : 'summary';
	});
	const setTab = (t: Tab) =>
		goto(`?tab=${t}`, { replaceState: true, noScroll: true, keepFocus: true });

	// One-shot handoff of a pending intent (context menu → "Edit settings" on an
	// unopened VM): consume and clear it, so a later visit doesn't replay it.
	let intent = $state<typeof ui.detailIntent>(null);
	$effect(() => {
		if (ui.detailIntent) {
			intent = ui.detailIntent;
			ui.detailIntent = null;
		}
	});
</script>

<Breadcrumb
	trail={[{ label: 'All VMs', href: '/compute' }, { label: namespace }, { label: name }]}
/>

{#if vm}
	<div class="min-h-0 flex-1 overflow-y-auto">
		<VMDetail
			{vm}
			{tab}
			ontab={setTab}
			onstaged={() => drafts.refresh()}
			stagedItem={drafts.stagedByKey.get(`${namespace}/${name}`) ?? null}
			onstagedopen={() => vm && (ui.modal = { kind: 'staged', vm })}
			onsearchlabel={(k, v) => ui.search?.searchFor(`label:${k}=${v}`)}
			networks={inventory.networks}
			{intent}
		/>
	</div>
{:else if inventory.inventory}
	<div class="flex flex-1 items-center justify-center text-sm text-ink-faint">
		{namespace}/{name} is not in the inventory (deleted, or not visible to you).
	</div>
{:else}
	<div class="flex flex-1 items-center justify-center text-sm text-ink-faint">Loading…</div>
{/if}
