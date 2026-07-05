<script lang="ts">
	import type { ProjectSync } from '$lib/api';
	import { projectSyncView } from '$lib/status';
	import Modal from './Modal.svelte';
	import StatusDot from './StatusDot.svelte';
	import StatusPill from './StatusPill.svelte';

	let { gitOps, compact = false }: { gitOps?: ProjectSync; compact?: boolean } = $props();

	// Null view = Synced+Healthy (or not-yet-known): the project needs no badge, so
	// green stays implicit and the tree isn't cluttered — the same rule as SyncBadge.
	const view = $derived(projectSyncView(gitOps));
	// Only a project ArgoCD couldn't apply has an error worth expanding.
	const clickable = $derived(!!gitOps?.syncError);
	let open = $state(false);

	function show(e: MouseEvent) {
		e.stopPropagation(); // don't also trigger row selection in the tree
		open = true;
	}

	const tip = $derived.by(() => {
		if (!gitOps) return '';
		const rev = gitOps.revision ? ` @ ${gitOps.revision}` : '';
		return (gitOps.syncError || `ArgoCD: ${view?.label ?? gitOps.sync}`) + rev;
	});
</script>

{#if view}
	{#if compact}
		<StatusDot tone={view.tone} size="xs" pulse={view.pulse} title={tip} />
	{:else}
		<StatusPill
			tone={view.tone}
			label={view.label}
			title={tip}
			onclick={clickable ? show : undefined}
		/>
	{/if}
{/if}

{#if open && gitOps}
	<Modal title="GitOps sync — {view?.label}" size="lg" onclose={() => (open = false)}>
		{#snippet icon()}<StatusDot tone={view?.tone ?? 'neutral'} />{/snippet}
		<div class="px-4 py-3 text-sm">
			<dl class="mb-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
				<dt class="text-ink-faint">Sync</dt>
				<dd>{gitOps.sync ?? '—'}</dd>
				<dt class="text-ink-faint">Health</dt>
				<dd>{gitOps.health ?? '—'}</dd>
				<dt class="text-ink-faint">Operation</dt>
				<dd>{gitOps.operation ?? '—'}</dd>
				{#if gitOps.revision}
					<dt class="text-ink-faint">Revision</dt>
					<dd>{gitOps.revision}</dd>
				{/if}
			</dl>
			{#if gitOps.syncError}
				<p class="mb-2 text-ink-soft">ArgoCD could not apply this project's manifests:</p>
				<pre
					class="max-h-72 overflow-auto rounded bg-danger-soft/60 p-3 text-xs whitespace-pre-wrap text-danger-ink">{gitOps.syncError}</pre>
			{:else}
				<p class="text-ink-soft">
					Objects in this project's repo differ from the cluster and ArgoCD hasn't finished applying
					the latest change. No apply error was reported — it's mid-sync or awaiting the next
					reconcile.
				</p>
			{/if}
		</div>
	</Modal>
{/if}
