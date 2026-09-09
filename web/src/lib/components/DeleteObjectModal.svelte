<script lang="ts">
	import { api } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';

	// The VM delete's confirm for every other git-declared object: stages the
	// removal of its manifest; the cluster object goes when the PR merges.
	let {
		resource,
		namespace,
		name,
		sourceFile,
		onclose,
		onstaged,
	}: {
		resource: string;
		namespace: string;
		name: string;
		sourceFile: string;
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	const op = action();
	async function confirm() {
		if (await op.run(() => api.deleteObject(resource, namespace, name))) {
			onstaged();
			onclose();
		}
	}
</script>

<ConfirmDelete
	title="Delete {name}"
	confirmWord={name}
	busy={op.busy}
	error={op.error}
	onconfirm={confirm}
	{onclose}
>
	<p>
		This removes <span class="font-mono text-xs">{sourceFile}</span> from git and stages the change
		into <strong>Changes</strong>. The object is deleted from the cluster only when the pull request
		is merged.
	</p>
</ConfirmDelete>
