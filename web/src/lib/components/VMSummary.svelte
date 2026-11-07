<script lang="ts">
	import { ChevronDown, ChevronRight } from '@lucide/svelte';
	import {
		api,
		vmKey,
		type Change,
		type Commit,
		type DraftItem,
		type VM,
		type VMUsage,
	} from '#lib/api.js';
	import { duration, relativeAge } from '#lib/format.js';
	import { resource } from '#lib/resource.svelte.js';
	import { itemKey } from '#lib/review.js';
	import CapacityUsage from './CapacityUsage.svelte';
	import Button from './Button.svelte';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import ChangeList from './ChangeList.svelte';
	import ConsolePreview from './ConsolePreview.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import InfoCard from './InfoCard.svelte';
	import VMConfigCard from './VMConfigCard.svelte';
	import Note from './Note.svelte';
	import Row from './Row.svelte';
	import StatusDot from './StatusDot.svelte';

	// The Summary tab body: at-a-glance tiles, live usage, guest/runtime cards,
	// and the two git-reconcile callouts (Not in git, Drift). Pure view over the
	// selected VM - adopt/resync/console stay the host's verbs.
	let {
		vm,
		stagedItem = null,
		driftChanges,
		reconciling,
		onadopt,
		onresync,
		onconsole,
		onedit,
		onmigrate,
	}: {
		vm: VM;
		stagedItem?: DraftItem | null;
		driftChanges: Change[] | null;
		reconciling: boolean;
		// Absent when the caller may not stage in this namespace.
		onadopt?: () => void;
		onresync: () => void;
		onconsole: () => void;
		// Opens Edit settings (the card header's next action); absent = read-only host.
		onedit?: () => void;
		// Opens the live-migration target picker (the Placement card's action).
		onmigrate?: () => void;
	} = $props();

	// A paused VMI keeps phase Running, so the label checks the Paused flag too.
	const statusText = $derived(vm.paused ? 'Paused' : (vm.phase ?? vm.power));

	const key = $derived(vmKey(vm));

	// One usage snapshot feeds the CPU/Memory tiles and the bars below, so both
	// always agree. Keyed on identity (the live stream hands down a fresh vm
	// object every frame); resource's stale guard drops an in-flight response
	// once the selection moves, so VM A's numbers never render under VM B.
	const usageRes = resource<VMUsage>(
		() => key,
		() => api.vmUsage(vm.namespace, vm.name),
		{
			poll: 30000,
			reset: true,
		},
	);
	const usage = $derived(usageRes.data);
	const usageLoading = $derived(usageRes.loading);
	const usageFailed = $derived(usageRes.failed);

	// The VM's latest merged change, for the GitOps card's one-line summary;
	// the Changes tab carries the full history. The project's applied revision
	// is in the key: a merge that reaches the cluster moves it.
	const project = $derived(inventory.projectOf(vm.namespace));
	const revision = $derived(
		inventory.projects.find((p) => p.name === project)?.gitOps?.revision ?? '',
	);
	const historyRes = resource<Commit[]>(
		() => `${key}|${vm.sourceFile ?? ''}|${revision}`,
		() => (vm.sourceFile ? api.vmHistory(vm.namespace, vm.name) : Promise.resolve([])),
		{ reset: true },
	);
	const latest = $derived(historyRes.data?.[0] ?? null);

	// Standing problems scoped to this VM (the Issues card), off the same
	// derivation the bell and the inspector use.
	const vmIssues = $derived(inventory.issues.filter((i) => i.scope === key));

	// Drift detail folds per selection, not per frame: key on identity.
	let showDrift = $state(false);
	$effect(() => {
		key;
		showDrift = false;
	});
</script>

<!-- Two columns: the configuration (git, live and staged per field) and the
     cards that carry a next action on the left; the guest, its placement and
     the console preview on the right. -->
<div class="grid items-start gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(17rem,1fr)]">
	<div class="flex min-w-0 flex-col gap-4">
		<VMConfigCard {vm} {stagedItem} {driftChanges} canAuthor={!!onedit} {onedit} />
		<div class="grid items-start gap-4 xl:grid-cols-2">
			<InfoCard title="GitOps">
				{#snippet action()}
					{#if stagedItem}
						<Button
							variant="link"
							size="sm"
							onclick={() => ui.openChanges({ kind: 'item', project, key: itemKey(stagedItem) })}
							>Review changes</Button
						>
					{/if}
				{/snippet}
				<div class="px-3 pt-2 pb-1">
					<GitOpsStepper
						stage={stagedItem ? 'staged' : vm.sync === 'Synced' ? 'synced' : 'merged'}
					/>
				</div>
				<dl class="divide-y divide-line-soft text-[13px]">
					<Row label="Source" value={vm.sourceFile} mono />
					<Row label="Sync"><SyncBadge sync={vm.sync} error={vm.syncError} /></Row>
					<Row
						label="Live vs git"
						value={driftChanges === null
							? '—'
							: driftChanges.length
								? `${driftChanges.length} field${driftChanges.length > 1 ? 's' : ''} differ — see below`
								: 'Identical'}
					/>
				</dl>
				{#if vm.sourceFile}
					<div class="flex items-center gap-2 border-t border-line-soft px-3 py-1.5 text-xs">
						{#if historyRes.loading}
							<span class="text-ink-faint">loading history…</span>
						{:else if historyRes.failed}
							<span class="text-danger-ink">{historyRes.error}</span>
						{:else if !latest}
							<span class="text-ink-faint">No merged changes yet.</span>
						{:else}
							<span class="min-w-0 truncate text-ink-soft" title={latest.title}
								>Last change: {latest.title}</span
							>
							<span class="shrink-0 text-ink-faint">{relativeAge(latest.when)}</span>
						{/if}
						<Button
							variant="link"
							class="ml-auto shrink-0"
							href="?tab=changes"
							data-sveltekit-replacestate>All changes</Button
						>
					</div>
				{/if}
			</InfoCard>
			<InfoCard title="Issues">
				{#if vmIssues.length === 0}
					<p class="flex items-center gap-2 px-3 py-2 text-xs text-ink-faint">
						<StatusDot tone="ok" size="xs" /> No standing problems.
					</p>
				{:else}
					<ul class="divide-y divide-line-soft">
						{#each vmIssues as i (i.label)}
							<li class="flex items-start gap-2 px-3 py-2 text-xs">
								<span class="mt-0.5"><StatusDot tone={i.severity} size="xs" /></span>
								<span class="text-ink-soft">{i.label}{i.detail ? ` — ${i.detail}` : ''}</span>
							</li>
						{/each}
					</ul>
				{/if}
			</InfoCard>
		</div>
		<CapacityUsage {usage} loading={usageLoading} failed={usageFailed} />
	</div>
	<div class="flex min-w-0 flex-col gap-4">
		<InfoCard title="Guest">
			{#snippet action()}
				{#if vm.phase === 'Running'}
					<Button variant="link" size="sm" onclick={onconsole}>Open console</Button>
				{/if}
			{/snippet}
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Operating system" value={vm.os ?? ''} />
				<Row label="Status" value={statusText} />
				<Row label="IP addresses">
					<div class="font-mono text-xs text-ink">
						{#if vm.ips?.length}
							{#each vm.ips as ip (ip)}<div>{ip}</div>{/each}
						{:else}{vm.guestIP || '—'}{/if}
					</div>
				</Row>
				<Row label="Uptime" value={vm.power === 'On' ? duration(vm.startedAt) : ''} />
			</dl>
		</InfoCard>
		<InfoCard title="Placement">
			{#snippet action()}
				{#if onmigrate && vm.phase === 'Running'}
					<Button variant="link" size="sm" onclick={onmigrate}>Migrate</Button>
				{/if}
			{/snippet}
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Host">
					{#if vm.nodeName}
						<a
							href="/hosts/{encodeURIComponent(vm.nodeName)}"
							class="text-accent-ink hover:underline">{vm.nodeName}</a
						>
					{:else}<span class="text-ink">—</span>{/if}
				</Row>
				<Row label="Pinned hosts" value={vm.scheduling?.pin?.join(', ') ?? ''} />
				<Row
					label="Placement groups"
					value={vm.scheduling?.groups?.map((g) => g.name).join(', ') ?? ''}
				/>
				<Row label="Balancer" value={vm.drsExclude ? 'Excluded' : 'Automatic'} />
				<Row label="Eviction strategy" value={vm.evictionStrategy || 'Cluster default'} />
			</dl>
		</InfoCard>
		<ConsolePreview {vm} onopen={() => onconsole()} />
	</div>
</div>

{#if !vm.sourceFile}
	<!-- Cluster-only VM (e.g. a fresh clone target): no manifest on the
	     base branch, so config stays read-only until adopted. The adopt
	     stages a CREATE of the running-branch manifest into the PR flow. -->
	<Note tone="warn" border class="mt-4">
		<div class="flex items-center gap-2 text-sm font-medium text-warn-ink">
			<StatusDot tone="warn" size="xs" />
			Not in git — this VM exists only in the cluster
		</div>
		<p class="mt-1 text-xs text-warn-ink">
			A VM created by Clone or outside dotvirt has no manifest in git yet, so settings edits and
			ArgoCD sync do not apply. Adopting stages its live manifest into
			<strong>Changes</strong>, to propose as a PR.
		</p>
		{#if onadopt}
			<div class="mt-2">
				<button
					onclick={onadopt}
					disabled={reconciling}
					title="Stage this VM's live manifest into a PR so git starts tracking it"
					class="rounded border border-warn/70 bg-panel px-2.5 py-1 text-xs font-medium text-warn-ink hover:bg-warn-soft disabled:opacity-50"
				>
					Adopt into git
				</button>
			</div>
		{/if}
	</Note>
{/if}

{#if driftChanges && driftChanges.length > 0}
	<div class="mt-4 rounded border border-warn-soft bg-warn-soft/60">
		<button
			onclick={() => (showDrift = !showDrift)}
			class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm font-medium text-warn-ink"
		>
			<StatusDot tone="warn" size="xs" />
			Drift — cluster differs from git ({driftChanges.length})
			<span class="ml-auto text-warn-ink"
				>{#if showDrift}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}</span
			>
		</button>
		{#if showDrift}
			<div class="border-t border-warn-soft px-3 py-2">
				<p class="mb-1 text-xs text-warn-ink">Desired (main) → Actual (running):</p>
				<ChangeList changes={driftChanges} />
				<div class="mt-3 flex items-center gap-2">
					{#if onadopt}
						<button
							onclick={onadopt}
							disabled={reconciling}
							title="Stage the live state into a PR so git matches the cluster"
							class="rounded border border-warn/70 bg-panel px-2.5 py-1 text-xs font-medium text-warn-ink hover:bg-warn-soft disabled:opacity-50"
						>
							Adopt into PR (running→main)
						</button>
					{/if}
					<button
						onclick={onresync}
						disabled={reconciling}
						title="Trigger ArgoCD to reconcile the cluster back to git"
						class="rounded border border-warn/70 bg-panel px-2.5 py-1 text-xs font-medium text-warn-ink hover:bg-warn-soft disabled:opacity-50"
					>
						Re-sync from git (main→running)
					</button>
				</div>
			</div>
		{/if}
	</div>
{/if}
