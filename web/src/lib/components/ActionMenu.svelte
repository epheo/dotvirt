<script lang="ts">
	// The shared action-menu panel: renders the VM-action registry for one VM.
	// Hosts own positioning (header dropdown, right-click menu) and perform the
	// picked action - this panel only displays and gates.
	import type { VM } from '$lib/api';
	import { vmActions, type VMAction } from '$lib/actions';

	let {
		vm,
		onpick,
		exclude = [],
	}: {
		vm: VM;
		onpick: (a: VMAction) => void;
		// Action ids the host already promotes as flat buttons - kept out of the
		// menu so a verb never appears twice in one header.
		exclude?: VMAction['id'][];
	} = $props();

	const actions = $derived(vmActions.filter((a) => !exclude.includes(a.id)));
</script>

<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
	{#each actions as a (a.id)}
		{#if a.sep}
			<div class="my-1 border-t border-line-soft"></div>
		{/if}
		<button
			onclick={() => onpick(a)}
			disabled={!a.enabled(vm)}
			title={a.title}
			class="block w-full px-3 py-1.5 text-left {a.danger
				? 'text-danger-ink hover:bg-danger-soft/60'
				: 'text-ink-soft hover:bg-inset'} disabled:cursor-not-allowed disabled:text-ink-faint disabled:hover:bg-transparent"
		>
			{a.label}
		</button>
	{/each}
</div>
