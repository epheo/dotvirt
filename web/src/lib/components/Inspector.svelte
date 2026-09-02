<script lang="ts">
	import { ChevronDown, Maximize2, X } from 'lucide-svelte';
	import { goto } from '$app/navigation';
	import type { VM } from '$lib/api';
	import { dispatchVMAction, vmActions } from '$lib/actions';
	import { vmHref } from '$lib/nav';
	import { duration } from '$lib/format';
	import { phaseTone } from '$lib/status';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ActionMenu from './ActionMenu.svelte';
	import HeaderMenu from './HeaderMenu.svelte';
	import StagedBadge from './StagedBadge.svelte';
	import StatusDot from './StatusDot.svelte';
	import StatusPill from './StatusPill.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';

	// The side peek: selecting a grid row inspects the VM here while the list
	// stays on screen; Enter (or the expand icon) opens the full page. Pure
	// cache read - everything shown rides the streamed inventory, so opening a
	// peek costs no cluster round-trip.
	let { vm, onclose }: { vm: VM; onclose: () => void } = $props();

	const key = $derived(`${vm.namespace}/${vm.name}`);
	const stagedItem = $derived(drafts.stagedByKey.get(key));
	const vmIssues = $derived(inventory.issues.filter((i) => i.scope === key));

	// Quick actions: the registry rows the mock's toolbar promotes; the rest
	// stay under the Actions menu so gating lives in one place.
	const quick = $derived(
		vmActions.filter((a) => ['console', 'migrate', 'edit'].includes(a.id) && a.enabled(vm)),
	);

	const openFull = () => goto(vmHref(vm.namespace, vm.name));

	function onkeydown(e: KeyboardEvent) {
		if (ui.modal || ui.ctx) return;
		const t = e.target as HTMLElement | null;
		if (t && t.closest('input, textarea, select, [contenteditable]')) return;
		if (e.key === 'Escape') {
			e.preventDefault();
			onclose();
		} else if (e.key === 'Enter') {
			e.preventDefault();
			openFull();
		}
	}

	// Resolve instancetype-backed sizing like the grid does, so the two agree.
	const flavor = $derived(
		vm.instancetype
			? inventory.options?.instancetypes?.find((i) => i.name === vm.instancetype)
			: undefined,
	);
	const cpu = $derived(vm.cpuCores ?? flavor?.cpu ?? (vm.vcpus || undefined));
	const mem = $derived(vm.memory ?? flavor?.memory ?? vm.memoryActual);
</script>

<svelte:window {onkeydown} />

<aside
	class="flex w-96 shrink-0 flex-col overflow-y-auto border-l border-line bg-panel"
	aria-label="VM inspector"
>
	<div class="flex items-center gap-2 border-b border-line px-3 py-2.5">
		<span class="min-w-0 truncate text-[15px] font-semibold text-ink">{vm.name}</span>
		<StatusPill
			tone={phaseTone(vm.phase, vm.paused)}
			label={vm.paused ? 'Paused' : (vm.phase ?? '—')}
		/>
		<span class="ml-auto flex shrink-0 items-center gap-0.5">
			<button
				onclick={openFull}
				title="Open full page (Enter)"
				class="rounded p-1.5 text-ink-faint hover:bg-inset hover:text-ink-soft"
			>
				<Maximize2 size={14} />
			</button>
			<button
				onclick={onclose}
				title="Close (Esc)"
				class="rounded p-1.5 text-ink-faint hover:bg-inset hover:text-ink-soft"
			>
				<X size={15} />
			</button>
		</span>
	</div>

	<div class="flex flex-wrap items-center gap-1 border-b border-line px-2 py-1.5">
		{#each quick as a (a.id)}
			<button
				onclick={() => dispatchVMAction(a, vm, { onstaged: () => drafts.refresh() })}
				title={a.title}
				class="rounded px-2 py-1 text-xs text-ink-soft hover:bg-inset"
			>
				{a.label.replace('…', '')}
			</button>
		{/each}
		<HeaderMenu class="ml-auto" align="right" panel={false}>
			{#snippet trigger({ toggle })}
				<button
					onclick={toggle}
					class="flex items-center gap-1 rounded px-2 py-1 text-xs text-ink-soft hover:bg-inset"
				>
					Actions <ChevronDown size={12} />
				</button>
			{/snippet}
			{#snippet children({ close })}
				<ActionMenu
					{vm}
					onpick={(a) => {
						close();
						dispatchVMAction(a, vm, { onstaged: () => drafts.refresh() });
					}}
				/>
			{/snippet}
		</HeaderMenu>
	</div>

	{#if vmIssues.length > 0}
		<div class="border-b border-line px-3 py-2">
			{#each vmIssues as i (i.label)}
				<div class="flex items-start gap-2 py-0.5 text-xs">
					<span class="mt-1"><StatusDot tone={i.severity} size="xs" /></span>
					<span class="text-ink-soft">{i.label}{i.detail ? ` — ${i.detail}` : ''}</span>
				</div>
			{/each}
		</div>
	{/if}

	<dl class="text-[13px]">
		<div class="px-3 pt-2 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase">
			Status
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">Host</dt>
			<dd class="text-ink">
				{#if vm.nodeName}<a
						href="/hosts/{encodeURIComponent(vm.nodeName)}"
						class="text-accent hover:underline">{vm.nodeName}</a
					>{:else}—{/if}
			</dd>
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">Guest IP</dt>
			<dd class="font-mono text-xs text-ink">{vm.guestIP ?? '—'}</dd>
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">Uptime</dt>
			<dd class="text-ink">{vm.startedAt && vm.power === 'On' ? duration(vm.startedAt) : '—'}</dd>
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">OS</dt>
			<dd class="text-ink">{vm.os ?? '—'}</dd>
		</div>

		<div class="px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase">
			GitOps
		</div>
		<div class="flex items-center gap-2 px-3 py-1.5">
			<GitOpsStepper stage={vm.sync === 'Synced' ? 'synced' : 'merged'} compact />
			<SyncBadge sync={vm.sync} error={vm.syncError} compact />
			{#if stagedItem}
				<StagedBadge item={stagedItem} onopen={() => (ui.modal = { kind: 'staged', vm })} />
			{/if}
			{#if stagedItem}
				<button
					onclick={() => ui.openChanges()}
					class="ml-auto text-xs text-accent-ink hover:underline">Review</button
				>
			{/if}
		</div>
		{#if vm.sourceFile}
			<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
				<dt class="text-ink-muted">Source</dt>
				<dd class="min-w-0 truncate font-mono text-xs text-ink" title={vm.sourceFile}>
					{vm.sourceFile}
				</dd>
			</div>
		{/if}

		<div class="px-3 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-ink-faint uppercase">
			Hardware
		</div>
		{#if vm.instancetype}
			<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
				<dt class="text-ink-muted">Instance type</dt>
				<dd class="text-ink">{vm.instancetype}</dd>
			</div>
		{/if}
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">vCPU / Memory</dt>
			<dd class="text-ink">{cpu ?? '—'} / {mem ?? '—'}</dd>
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">Disks</dt>
			<dd class="text-ink">
				{vm.disks?.length ?? 0}
				{#if vm.disks?.some((d) => d.size)}
					<span class="text-ink-faint"
						>({vm.disks
							.filter((d) => d.size)
							.map((d) => d.size)
							.join(', ')})</span
					>
				{/if}
			</dd>
		</div>
		<div class="flex justify-between gap-3 border-b border-line-soft px-3 py-1.5">
			<dt class="text-ink-muted">Networks</dt>
			<dd class="min-w-0 truncate text-ink">
				{vm.networks?.length ? vm.networks.map((n) => n.network || n.name).join(', ') : '—'}
			</dd>
		</div>
	</dl>

	<div
		class="mt-auto flex items-center gap-3 border-t border-line px-3 py-2 text-[11px] text-ink-faint"
	>
		<span><kbd class="rounded border border-line-strong bg-inset px-1">Enter</kbd> full page</span>
		<span><kbd class="rounded border border-line-strong bg-inset px-1">Esc</kbd> close</span>
	</div>
</aside>
