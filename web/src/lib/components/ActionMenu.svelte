<script lang="ts">
	// The shared action-menu panel: renders the VM-action registry for one VM.
	// Hosts own positioning (header dropdown, right-click menu) and perform the
	// picked action — this panel only displays and gates.
	import type { VM } from '$lib/api';
	import { vmActions, type VMAction } from '$lib/actions';

	let {
		vm,
		onpick,
	}: {
		vm: VM;
		onpick: (a: VMAction) => void;
	} = $props();
</script>

<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
	{#each vmActions as a (a.id)}
		{#if a.sep}
			<div class="my-1 border-t border-line-soft"></div>
		{/if}
		<button
			onclick={() => onpick(a)}
			disabled={!a.enabled(vm)}
			title={a.title}
			class="block w-full px-3 py-1.5 text-left {a.danger
				? 'text-red-700 hover:bg-red-50'
				: 'text-ink-soft hover:bg-inset'} disabled:cursor-not-allowed disabled:text-ink-faint disabled:hover:bg-transparent"
		>
			{a.label}
		</button>
	{/each}
</div>
