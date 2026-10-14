<script lang="ts">
	import type { Network, VM } from '#lib/api.js';
	import type { EditForm, NicRow } from '#lib/editform.js';
	import { kindLabel, attachRef } from '#lib/networks.js';
	import RowListEditor from '../RowListEditor.svelte';
	import SelectInput from '../SelectInput.svelte';

	let {
		form = $bindable(),
		vm,
		available,
	}: { form: EditForm; vm: VM; available: Network[] } = $props();

	function addNic() {
		const first = available[0];
		form.nics = [
			...form.nics,
			{
				name: first ? first.name : 'net1',
				network: first ? attachRef(first) : '',
				removed: false,
				isNew: true,
			},
		];
	}
</script>

{#snippet nicRow(nic: NicRow)}
	{#if nic.isNew}
		<SelectInput bind:value={nic.network} size="sm" width="w-60">
			{#each available as net (net.scope + (net.namespace ?? '') + net.name)}
				<option value={attachRef(net)}
					>{net.name} — {kindLabel(net.kind)}{net.scope === 'shared' ? ' · shared' : ''}</option
				>
			{/each}
		</SelectInput>
	{:else}
		<span class="text-xs text-ink-muted">{nic.name} ({nic.network})</span>
	{/if}
{/snippet}
<RowListEditor
	items={form.nics}
	unit="adapter(s)"
	addLabel="+ Add network adapter"
	rowLabel="Network adapter"
	empty="No adapters."
	onadd={addNic}
	ontoggle={(n) => (n.removed = !n.removed)}
	row={nicRow}
/>
{#if vm.networks?.some((n) => n.implicit)}
	<p class="mt-2 text-xs text-ink-faint">
		The pod network adapter is attached by KubeVirt while the manifest declares none; it is not
		edited here.
	</p>
{/if}
