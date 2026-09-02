<script lang="ts">
	import { ChevronDown, ChevronRight } from 'lucide-svelte';
	import { api, type Change, type DraftItem, type VM, type VMUsage } from '$lib/api';
	import { duration } from '$lib/format';
	import { resource } from '$lib/resource.svelte';
	import CapacityUsage from './CapacityUsage.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ChangeList from './ChangeList.svelte';
	import ConsolePreview from './ConsolePreview.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import SyncBadge from './SyncBadge.svelte';
	import InfoCard from './InfoCard.svelte';
	import Note from './Note.svelte';
	import Row from './Row.svelte';
	import StagedDiff from './StagedDiff.svelte';
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
		onmonitor,
		onedit,
		onmigrate,
	}: {
		vm: VM;
		stagedItem?: DraftItem | null;
		driftChanges: Change[] | null;
		reconciling: boolean;
		onadopt: () => void;
		onresync: () => void;
		onconsole: () => void;
		onmonitor: () => void;
		// Opens Edit settings (the card header's next action); absent = read-only host.
		onedit?: () => void;
		// Opens the live-migration target picker (the Placement card's action).
		onmigrate?: () => void;
	} = $props();

	// A paused VMI keeps phase Running, so the label checks the Paused flag too.
	const statusText = $derived(vm.paused ? 'Paused' : (vm.phase ?? vm.power));

	const vmKey = $derived(`${vm.namespace}/${vm.name}`);

	// One usage snapshot feeds the CPU/Memory tiles and the bars below, so both
	// always agree. Keyed on identity (the live stream hands down a fresh vm
	// object every frame); resource's stale guard drops an in-flight response
	// once the selection moves, so VM A's numbers never render under VM B.
	const usageRes = resource<VMUsage>(
		() => vmKey,
		() => api.vmUsage(vm.namespace, vm.name),
		{
			poll: 30000,
			reset: true,
		},
	);
	const usage = $derived(usageRes.data);
	const usageLoading = $derived(usageRes.loading);
	const usageFailed = $derived(usageRes.failed);

	// The manifest owns sizing when present; an instancetype-sized VM carries no
	// cpuCores/memory in git, so the tiles fall back to the rendered topology.
	const cpuVal = $derived(vm.cpuCores ?? (vm.vcpus || undefined));
	const memVal = $derived(vm.memory ?? vm.memoryActual);

	// Staged changes for this VM, keyed by field label (for inline current->future).
	const stagedChanges = $derived.by(() => {
		const m = new Map<string, Change>();
		for (const c of stagedItem?.changes ?? []) m.set(c.field, c);
		return m;
	});

	// Standing problems scoped to this VM (the Issues card), off the same
	// derivation the bell and the inspector use.
	const vmIssues = $derived(inventory.issues.filter((i) => i.scope === vmKey));

	// Drift detail folds per selection, not per frame: key on identity.
	let showDrift = $state(false);
	$effect(() => {
		vmKey;
		showDrift = false;
	});
</script>

<!-- The object fact grid (the review-screen idiom): six cards, each carrying
     its next action, with the live console preview beside them. Usage bars and
     sparklines live in the Capacity card; staged values render inline as
     current -> future wherever the field appears. -->
<div class="flex flex-col gap-4 xl:flex-row xl:items-start">
	<div class="grid min-w-0 flex-1 gap-4 md:grid-cols-2">
		<InfoCard title="Guest">
			{#snippet action()}
				{#if vm.phase === 'Running'}
					<button onclick={onconsole} class="text-xs text-accent-ink hover:underline"
						>Console</button
					>
				{/if}
			{/snippet}
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Operating system" value={vm.os ?? ''} />
				<Row label="Power (desired)">
					{#if stagedChanges.has('Power')}
						<StagedDiff from={vm.power} to={stagedChanges.get('Power')?.to ?? ''} />
					{:else}<span class="text-ink">{vm.power}</span>{/if}
				</Row>
				<Row label="Status (actual)" value={statusText} />
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

		<InfoCard title="Hardware">
			{#snippet action()}
				{#if onedit && vm.sourceFile}
					<button onclick={onedit} class="text-xs text-accent-ink hover:underline">Edit</button>
				{/if}
			{/snippet}
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Instance type" value={vm.instancetype ?? ''} />
				<Row label="Preference" value={vm.preference ?? ''} />
				<Row label="vCPU">
					{#if stagedChanges.has('CPU')}
						<StagedDiff from={`${cpuVal ?? '—'}`} to={stagedChanges.get('CPU')?.to ?? ''} />
					{:else}<span class="text-ink">{cpuVal ?? '—'}</span>{/if}
				</Row>
				<Row label="Memory">
					{#if stagedChanges.has('Memory')}
						<StagedDiff from={memVal ?? '—'} to={stagedChanges.get('Memory')?.to ?? ''} />
					{:else}<span class="text-ink">{memVal ?? '—'}</span>{/if}
				</Row>
				<Row label="Disks">
					<span class="text-ink">
						{vm.disks?.length ?? 0}
						{#if vm.disks?.some((d) => d.size)}
							<span class="text-ink-faint"
								>({vm.disks
									.filter((d) => d.size)
									.map((d) => d.size)
									.join(', ')})</span
							>
						{/if}
					</span>
				</Row>
				<Row
					label="Networks"
					value={vm.networks?.length ? vm.networks.map((n) => n.network || n.name).join(', ') : ''}
				/>
			</dl>
		</InfoCard>

		<InfoCard title="Placement">
			{#snippet action()}
				{#if onmigrate && vm.phase === 'Running'}
					<button onclick={onmigrate} class="text-xs text-accent-ink hover:underline"
						>Migrate</button
					>
				{/if}
			{/snippet}
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Host">
					{#if vm.nodeName}
						<a href="/hosts/{encodeURIComponent(vm.nodeName)}" class="text-accent hover:underline"
							>{vm.nodeName}</a
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

		<InfoCard title="GitOps">
			{#snippet action()}
				{#if stagedItem}
					<button onclick={() => ui.openChanges()} class="text-xs text-accent-ink hover:underline"
						>Review changes</button
					>
				{/if}
			{/snippet}
			<div class="px-3 pt-2 pb-1">
				<GitOpsStepper stage={stagedItem ? 'staged' : vm.sync === 'Synced' ? 'synced' : 'merged'} />
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

		<CapacityUsage {usage} loading={usageLoading} failed={usageFailed} />
	</div>
	<ConsolePreview {vm} onopen={() => onconsole()} />
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
			A clone target (or out-of-band create) has no manifest on the base branch yet: config edits
			and ArgoCD sync don't apply. Adopting stages its live manifest into
			<strong>Changes</strong>, to propose as a PR.
		</p>
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
					<button
						onclick={onadopt}
						disabled={reconciling}
						title="Stage the live state into a PR so git matches the cluster"
						class="rounded border border-warn/70 bg-panel px-2.5 py-1 text-xs font-medium text-warn-ink hover:bg-warn-soft disabled:opacity-50"
					>
						Adopt into PR (running→main)
					</button>
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
