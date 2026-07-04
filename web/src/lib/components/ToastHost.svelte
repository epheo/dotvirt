<script lang="ts">
	import { CircleAlert, CircleCheck, Info, X } from 'lucide-svelte';
	import { ui } from '$lib/state/ui.svelte';
</script>

{#if ui.toasts.length}
	<div class="fixed bottom-4 left-1/2 z-50 flex -translate-x-1/2 flex-col items-center gap-2">
		{#each ui.toasts as t (t.id)}
			<!-- The pill rides the always-dark bar surface, so its icon colors stay
			     raw 400-level hues rather than the light-theme status tokens. -->
			<div
				class="flex items-center gap-2.5 rounded-md bg-bar px-4 py-2 text-sm text-white shadow-lg"
			>
				{#if t.kind === 'success'}<CircleCheck size={15} class="shrink-0 text-emerald-400" />
				{:else if t.kind === 'error'}<CircleAlert size={15} class="shrink-0 text-red-400" />
				{:else}<Info size={15} class="shrink-0 text-slate-400" />{/if}
				{t.msg}
				{#if t.action}
					{@const action = t.action}
					<button
						onclick={() => {
							action.run();
							ui.dismissToast(t.id);
						}}
						class="shrink-0 rounded border border-slate-500 px-2 py-0.5 text-xs font-medium hover:bg-slate-700"
					>
						{action.label}
					</button>
				{/if}
				<button
					onclick={() => ui.dismissToast(t.id)}
					aria-label="Dismiss"
					class="shrink-0 text-slate-400 hover:text-white"><X size={14} /></button
				>
			</div>
		{/each}
	</div>
{/if}
