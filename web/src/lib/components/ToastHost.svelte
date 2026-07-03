<script lang="ts">
	import { ui } from '$lib/state/ui.svelte';
</script>

{#if ui.toast}
	<div
		class="fixed bottom-4 left-1/2 z-50 flex -translate-x-1/2 items-center gap-3 rounded-md bg-bar px-4 py-2 text-sm text-white shadow-lg"
	>
		{ui.toast.msg}
		{#if ui.toast.action}
			{@const action = ui.toast.action}
			<button
				onclick={() => {
					action.run();
					ui.toast = null;
				}}
				class="shrink-0 rounded border border-slate-500 px-2 py-0.5 text-xs font-medium hover:bg-slate-700"
			>
				{action.label}
			</button>
		{/if}
	</div>
{/if}
