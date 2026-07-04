<script lang="ts">
	import { Power, PowerOff, Trash2 } from 'lucide-svelte';

	// The grid's bulk-selection bar. Every action stages a change through the
	// PR flow (nothing touches the cluster); the host owns the selection set
	// and the staging calls.
	let {
		count,
		busy = false,
		onpower,
		ondelete,
		onclear,
	}: {
		count: number;
		busy?: boolean;
		onpower: (state: 'On' | 'Off') => void;
		ondelete: () => void;
		onclear: () => void;
	} = $props();
</script>

<div class="flex items-center gap-2 border-b border-line bg-select-soft px-4 py-1.5 text-sm">
	<span class="font-medium text-ink-soft">{count} selected</span>
	<span class="text-ink-faint">|</span>
	<button
		onclick={() => onpower('On')}
		disabled={busy}
		class="flex items-center gap-1.5 rounded border border-line-strong bg-panel px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
	>
		<Power size={13} class="text-ok-ink" /> Power On
	</button>
	<button
		onclick={() => onpower('Off')}
		disabled={busy}
		class="flex items-center gap-1.5 rounded border border-line-strong bg-panel px-2.5 py-1 text-xs font-medium text-ink-soft hover:bg-inset disabled:opacity-50"
	>
		<PowerOff size={13} class="text-ink-muted" /> Power Off
	</button>
	<button
		onclick={ondelete}
		disabled={busy}
		class="flex items-center gap-1.5 rounded border border-danger/50 bg-panel px-2.5 py-1 text-xs font-medium text-danger-ink hover:bg-danger-soft/60 disabled:opacity-50"
	>
		<Trash2 size={13} /> Delete
	</button>
	<button onclick={onclear} class="ml-auto text-xs text-ink-muted hover:text-ink-soft">
		Clear
	</button>
</div>
