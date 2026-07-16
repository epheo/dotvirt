<script lang="ts">
	import type { VM } from '$lib/api';
	import ConfirmDelete from '$lib/components/ConfirmDelete.svelte';

	// Type-to-confirm dialog enumerating the VMs about to be delete-staged.
	let {
		vms,
		busy,
		onconfirm,
		onclose,
	}: {
		vms: VM[];
		busy: boolean;
		onconfirm: () => void;
		onclose: () => void;
	} = $props();
</script>

<ConfirmDelete title="Delete {vms.length} VMs" confirmWord="delete" {busy} {onconfirm} {onclose}>
	<p class="mb-3">
		This stages removal of the following VMs into <strong>Changes</strong>. They are deleted from
		the cluster only when each project's PR is merged.
	</p>
	<ul class="max-h-40 overflow-y-auto rounded border border-line text-xs">
		{#each vms as vm (vm.namespace + '/' + vm.name)}
			<li class="border-b border-line-soft px-2 py-1 last:border-0">
				<span class="font-medium text-ink">{vm.name}</span>
				<span class="text-ink-faint">· {vm.namespace}</span>
			</li>
		{/each}
	</ul>
</ConfirmDelete>
