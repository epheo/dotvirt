<script lang="ts">
	import { api, type VM } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';

	// Delete is destructive once the PR merges, so it is gated behind a confirm
	// that requires typing the VM name.
	let { vm, onclose, onstaged }: { vm: VM; onclose: () => void; onstaged: () => void } = $props();

	const op = action();
	async function confirm() {
		if (await op.run(() => api.stageDelete(vm.namespace, vm.name))) {
			onstaged();
			onclose();
		}
	}
</script>

<ConfirmDelete
	title="Delete VM — {vm.name}"
	confirmWord={vm.name}
	busy={op.busy}
	error={op.error}
	onconfirm={confirm}
	{onclose}
>
	<p>
		This removes <span class="font-mono text-xs">{vm.sourceFile}</span> from git and stages the
		change into <strong>Changes</strong>. The VM is deleted from the cluster only when the pull
		request is merged.
	</p>
</ConfirmDelete>
