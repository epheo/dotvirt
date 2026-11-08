<script lang="ts">
	import ContextMenu from '#lib/components/ContextMenu.svelte';
	import MenuItem from '#lib/components/MenuItem.svelte';

	// Verb list for a right-click inside the multi-selection. Deliberately not
	// driven by the VM action registry: Power isn't a registry action, and these
	// stage batch edits, not runtime ops.
	let {
		x,
		y,
		count,
		powerWhy = '',
		deleteWhy = '',
		onclose,
		onpower,
		ondelete,
		onclear,
	}: {
		x: number;
		y: number;
		count: number;
		// Why none of the selection may be powered or deleted; '' when some may.
		powerWhy?: string;
		deleteWhy?: string;
		onclose: () => void;
		onpower: (target: 'On' | 'Off') => void;
		ondelete: () => void;
		onclear: () => void;
	} = $props();
</script>

<ContextMenu {x} {y} {onclose}>
	<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
		<div class="px-3 py-1 text-[10px] tracking-wide text-ink-faint uppercase">
			{count} VMs selected
		</div>
		<MenuItem
			disabled={!!powerWhy}
			title={powerWhy}
			onclick={() => {
				onclose();
				onpower('On');
			}}>Power On (staged)</MenuItem
		>
		<MenuItem
			disabled={!!powerWhy}
			title={powerWhy}
			onclick={() => {
				onclose();
				onpower('Off');
			}}>Power Off (staged)</MenuItem
		>
		<div class="my-1 border-t border-line-soft"></div>
		<MenuItem
			danger
			disabled={!!deleteWhy}
			title={deleteWhy}
			onclick={() => {
				onclose();
				ondelete();
			}}>Delete {count} VMs…</MenuItem
		>
		<div class="my-1 border-t border-line-soft"></div>
		<MenuItem
			onclick={() => {
				onclose();
				onclear();
			}}>Clear selection</MenuItem
		>
	</div>
</ContextMenu>
