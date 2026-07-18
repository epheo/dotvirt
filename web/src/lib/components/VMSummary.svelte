<script lang="ts">
	import { untrack } from 'svelte';
	import { Activity, ChevronDown, ChevronRight, Cpu, HardDrive, MemoryStick } from 'lucide-svelte';
	import { api, Unauthorized, type Change, type DraftItem, type VM, type VMUsage } from '$lib/api';
	import { duration, fmtUsage } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';
	import CapacityUsage from './CapacityUsage.svelte';
	import ChangeList from './ChangeList.svelte';
	import ConsolePreview from './ConsolePreview.svelte';
	import InfoCard from './InfoCard.svelte';
	import Row from './Row.svelte';
	import Sparkline from './Sparkline.svelte';
	import StagedDiff from './StagedDiff.svelte';
	import StatusDot from './StatusDot.svelte';

	// The Summary tab body: at-a-glance tiles, live usage, guest/runtime cards,
	// and the two git-reconcile callouts (Not in git, Drift). Pure view over the
	// selected VM — adopt/resync/console stay the host's verbs.
	let {
		vm,
		stagedItem = null,
		driftChanges,
		reconciling,
		onadopt,
		onresync,
		onconsole,
		onmonitor,
	}: {
		vm: VM;
		stagedItem?: DraftItem | null;
		driftChanges: Change[] | null;
		reconciling: boolean;
		onadopt: () => void;
		onresync: () => void;
		onconsole: () => void;
		onmonitor: () => void;
	} = $props();

	// A paused VMI keeps phase Running, so the label checks the Paused flag too.
	const statusText = $derived(vm.paused ? 'Paused' : (vm.phase ?? vm.power));

	const vmKey = $derived(`${vm.namespace}/${vm.name}`);

	// One usage snapshot feeds the CPU/Memory tiles and the bars below, so both
	// always agree. Keyed on identity — the live stream hands down a fresh vm
	// object every frame; untrack keeps load()'s reads from re-firing.
	let usage = $state<VMUsage | null>(null);
	let usageLoading = $state(false);
	let usageFailed = $state(false);
	async function loadUsage() {
		usageLoading = true;
		try {
			usage = await api.vmUsage(vm.namespace, vm.name);
			usageFailed = false;
		} catch (e) {
			if (e instanceof Unauthorized) return;
			usageFailed = true;
		} finally {
			usageLoading = false;
		}
	}
	$effect(() => {
		vmKey;
		usage = null;
		untrack(loadUsage);
	});
	$effect(() => pollWhileVisible(loadUsage, 30000));

	// The manifest owns sizing when present; an instancetype-sized VM carries no
	// cpuCores/memory in git, so the tiles fall back to the rendered topology.
	const cpuVal = $derived(vm.cpuCores ?? (vm.vcpus || undefined));
	const memVal = $derived(vm.memory ?? vm.memoryActual);

	// Staged changes for this VM, keyed by field label (for inline current→future).
	const stagedChanges = $derived.by(() => {
		const m = new Map<string, Change>();
		for (const c of stagedItem?.changes ?? []) m.set(c.field, c);
		return m;
	});

	// Drift detail folds per selection, not per frame: key on identity.
	let showDrift = $state(false);
	$effect(() => {
		vmKey;
		showDrift = false;
	});
</script>

<!-- At-a-glance tiles + live usage on the left, the console preview spanning
     both on the right (running VMs only) — the preview emits no DOM when
     hidden, so the left column reclaims the full width. CPU/Memory tiles
     carry the live trend and click through to Monitor. -->
<div class="flex flex-col gap-4 xl:flex-row xl:items-stretch">
	<div class="min-w-0 flex-1">
		<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
			<button
				onclick={onmonitor}
				title="Open the Monitor tab"
				class="rounded border border-line bg-inset p-3 text-left hover:border-line-strong"
			>
				<div class="flex items-center gap-1.5 text-xs text-ink-muted">
					<Cpu size={13} /> CPU
				</div>
				<div class="mt-1 text-lg font-semibold text-ink">
					{#if stagedChanges.has('CPU')}
						<StagedDiff from={`${cpuVal ?? '—'} vCPU`} to={stagedChanges.get('CPU')?.to ?? ''} />
					{:else if cpuVal}
						{cpuVal}<span class="ml-1 text-sm font-normal text-ink-muted">vCPU</span>
					{:else if vm.instancetype}
						<span class="text-sm font-medium" title="Sized by the instance type"
							>{vm.instancetype}</span
						>
					{:else}—{/if}
				</div>
				{#if usage}
					<div class="mt-1 flex items-center gap-2 text-xs text-ink-faint">
						<Sparkline values={usage.cpu.spark ?? []} color="var(--chart-1)" height={14} />
						{Math.round(usage.cpu.used)}% used
					</div>
				{/if}
			</button>
			<button
				onclick={onmonitor}
				title="Open the Monitor tab"
				class="rounded border border-line bg-inset p-3 text-left hover:border-line-strong"
			>
				<div class="flex items-center gap-1.5 text-xs text-ink-muted">
					<MemoryStick size={13} /> Memory
				</div>
				<div class="mt-1 text-lg font-semibold text-ink">
					{#if stagedChanges.has('Memory')}
						<StagedDiff from={memVal ?? '—'} to={stagedChanges.get('Memory')?.to ?? ''} />
					{:else if memVal}
						{memVal}
					{:else if vm.instancetype}
						<span class="text-sm font-medium" title="Sized by the instance type"
							>{vm.instancetype}</span
						>
					{:else}—{/if}
				</div>
				{#if usage && usage.memory.total}
					<div class="mt-1 flex items-center gap-2 text-xs text-ink-faint">
						<Sparkline values={usage.memory.spark ?? []} color="var(--chart-2)" height={14} />
						{fmtUsage('bytes', usage.memory.used)} used
					</div>
				{:else if vm.memoryActual && vm.memory && vm.memoryActual !== vm.memory}
					<div class="mt-1 text-xs text-ink-faint">{vm.memoryActual} live</div>
				{/if}
			</button>
			<div class="rounded border border-line bg-inset p-3">
				<div class="flex items-center gap-1.5 text-xs text-ink-muted">
					<HardDrive size={13} /> Disks
				</div>
				<div class="mt-1 text-lg font-semibold text-ink">{vm.disks?.length ?? 0}</div>
			</div>
			<div class="rounded border border-line bg-inset p-3">
				<div class="flex items-center gap-1.5 text-xs text-ink-muted">
					<Activity size={13} /> Status
				</div>
				<div class="mt-1 text-lg font-semibold text-ink">{statusText}</div>
				{#if duration(vm.startedAt)}<div class="text-xs text-ink-faint">
						up {duration(vm.startedAt)}
					</div>{/if}
			</div>
		</div>
		<div class="mt-4">
			<CapacityUsage {usage} loading={usageLoading} failed={usageFailed} />
		</div>
	</div>
	<ConsolePreview {vm} onopen={() => onconsole()} />
</div>

<div class="mt-4 grid gap-4 md:grid-cols-2">
	<!-- Guest & runtime: live identity reported by the guest agent. -->
	<InfoCard title="Guest & runtime">
		<dl class="divide-y divide-line-soft text-[13px]">
			<Row label="Operating system" value={vm.os ?? ''} />
			<Row label="Power (desired)">
				{#if stagedChanges.has('Power')}
					<StagedDiff from={vm.power} to={stagedChanges.get('Power')?.to ?? ''} />
				{:else}<span class="text-ink">{vm.power}</span>{/if}
			</Row>
			<Row label="Status (actual)" value={vm.paused ? 'Paused' : (vm.phase ?? '')} />
			<Row label="IP addresses">
				<div class="font-mono text-xs text-ink">
					{#if vm.ips?.length}
						{#each vm.ips as ip (ip)}<div>{ip}</div>{/each}
					{:else}{vm.guestIP || '—'}{/if}
				</div>
			</Row>
		</dl>
	</InfoCard>

	<!-- Configuration & placement: desired config + where it runs. -->
	<InfoCard title="Configuration & placement">
		<dl class="divide-y divide-line-soft text-[13px]">
			<Row label="Instance type" value={vm.instancetype ?? ''} />
			<Row label="Preference" value={vm.preference ?? ''} />
			<Row label="Node" value={vm.nodeName ?? ''} />
			<Row label="Source" value={vm.sourceFile} mono />
		</dl>
	</InfoCard>
</div>

{#if !vm.sourceFile}
	<!-- Cluster-only VM (e.g. a fresh clone target): no manifest on the
	     base branch, so config stays read-only until adopted. The adopt
	     stages a CREATE of the running-branch manifest into the PR flow. -->
	<div class="mt-4 rounded border border-warn-soft bg-warn-soft/60 px-3 py-2">
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
	</div>
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
