<script lang="ts">
	import { Ban, Globe, GitMerge, Plug, Plus, ShieldCheck } from '@lucide/svelte';
	import {
		api,
		type Commit,
		type Network,
		type NetworkRules,
		type NetworkTraffic,
		type VM,
	} from '#lib/api.js';
	import { relativeAge } from '#lib/format.js';
	import { POD_NETWORK, vmNetworkKeys } from '#lib/lenses.js';
	import { uplinkLabel } from '#lib/netmap.js';
	import { networkHref, vmHref } from '#lib/nav.js';
	import { networkKey } from '#lib/networks.js';
	import {
		canAdoptNetwork,
		canAuthorRef,
		canDeleteNetwork,
		canEditNetwork,
		networkRef,
		openAdopt,
		openDelete,
		openEdit,
	} from '#lib/objects.js';
	import { resource } from '#lib/resource.svelte.js';
	import { topLines, type RuleLine } from '#lib/rulelines.js';
	import { overlaps, usableAddresses } from '#lib/subnets.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import AttachVMsModal from './AttachVMsModal.svelte';
	import DeleteNetworkModal from './DeleteNetworkModal.svelte';
	import RuleList from './RuleList.svelte';

	// A network's Summary: where it connects (its way out and what its rules
	// let it reach), the rules that apply, recent changes to its file, how full
	// its range is, what can change, and its busiest VMs.
	let {
		net,
		nkey,
		vms,
		project,
		rules,
		lines,
		rulesFailed,
	}: {
		net: Network | null; // null for the built-in pod network
		nkey: string;
		vms: VM[];
		project: string;
		rules: NetworkRules | null | undefined;
		lines: RuleLine[];
		rulesFailed: boolean;
	} = $props();

	const fmt = (n: number) => n.toLocaleString('en-US');
	const name = $derived(net?.name ?? POD_NETWORK);
	const builtin = $derived(inventory.uplinks.find((u) => u.builtin));
	const vlanUplink = $derived(
		net?.kind === 'vlan' ? inventory.uplinks.find((u) => u.name === net.uplink) : undefined,
	);
	// How the network leaves: the primary ones (and the pod network) through
	// the default uplink and the cluster edge, VLANs through their uplink to the
	// fabric. A secondary overlay has no gateway of its own: its VMs leave
	// through their main network, drawn as such.
	const via = $derived(net?.kind === 'vlan' ? 'fabric' : 'edge');
	const throughMain = $derived(!!net && net.kind === 'internal');

	// --- what it reaches ---
	type Reach = { tone: 'allow' | 'shared' | 'blocked'; title: string; sub: string; href?: string };
	const portsWords = (p?: string) => (p ? p.replaceAll('/', ' ') : 'All traffic');
	function subjectWords(ns: string | undefined, target: string | undefined): string {
		if (!target || target === 'all pods') return `every VM${ns ? ` in ${ns}` : ''}`;
		const m = /^([^=,\s]+)=([^,\s]+)$/.exec(target);
		const picked = m
			? inventory.allVMs.filter((v) => v.namespace === ns && v.labels?.[m[1]] === m[2])
			: [];
		return picked.length === 1 ? `${picked[0].name} only` : `VMs labeled ${target}`;
	}
	const reaches = $derived.by((): Reach[] => {
		const out: Reach[] = [];
		const byProject = new Map<string, Reach>();
		for (const r of rules?.reaches ?? []) {
			if (r.rule.direction !== 'Ingress' || r.rule.action !== 'Allow') continue;
			const ns = r.policy.namespace;
			const title = ns ? inventory.projectOf(ns) || ns : r.policy.name;
			if (!byProject.has(title))
				byProject.set(title, {
					tone: 'allow',
					title,
					sub: `${portsWords(r.rule.ports)} to ${subjectWords(ns, r.policy.target)}`,
				});
		}
		out.push(...[...byProject.values()].slice(0, 1));
		// Networks some of its VMs also sit on.
		const both = new Map<string, number>();
		for (const v of vms)
			for (const k of vmNetworkKeys(v, inventory.networks))
				if (k !== nkey && k !== POD_NETWORK) both.set(k, (both.get(k) ?? 0) + 1);
		const shared = [...both.entries()].sort((a, b) => b[1] - a[1])[0];
		if (shared) {
			const other = networking.byKey.get(shared[0]);
			out.push({
				tone: 'shared',
				title: other?.name ?? shared[0],
				sub: `${other?.kind === 'vlan' ? `VLAN ${other.vlan ?? ''}. ` : ''}${shared[1] === vms.length ? 'All' : 'Some'} VMs on both.`,
				href: networkHref(shared[0]),
			});
		}
		const deny = (rules?.applies ?? []).find(
			(b) =>
				b.policy.kind === 'admin' &&
				(b.policy.rules ?? []).some(
					(r) => r.direction === 'Ingress' && r.action === 'Deny' && r.peer?.startsWith('ns any'),
				),
		);
		if (deny) {
			const others = Math.max(0, inventory.projects.length - 1);
			out.push({
				tone: 'blocked',
				title: `${fmt(others)} other project${others === 1 ? '' : 's'}`,
				sub: `Blocked by ${deny.policy.name}`,
			});
		}
		if (out.length < 3) out.push(...[...byProject.values()].slice(1, 1 + 3 - out.length));
		return out.slice(0, 3);
	});

	// --- recent changes ---
	const ref = $derived(net?.sourceFile ? networkRef(net) : null);
	const histRes = resource<Commit[] | null>(
		() => (ref ? `${ref.resource}/${ref.namespace}/${ref.name}|${inventory.networksVersion}` : ''),
		() => (ref ? api.objectHistory(ref.resource, ref.namespace, ref.name) : Promise.resolve(null)),
	);

	// "3 days ago" for the last week, then the date: how a merge reads in a list.
	function mergedWhen(iso: string): string {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t)) return '';
		const h = Math.floor((Date.now() - t) / 36e5);
		if (h < 1) return 'just now';
		if (h < 24) return `${h} hour${h === 1 ? '' : 's'} ago`;
		const d = Math.floor(h / 24);
		if (d < 7) return `${d} day${d === 1 ? '' : 's'} ago`;
		return new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
	}

	// --- address use ---
	const subnet = $derived(net?.subnets?.[0] ?? '');
	const capacity = $derived(subnet ? usableAddresses(subnet) : 0);
	const used = $derived(vms.length);
	const clash = $derived(
		net && subnet
			? inventory.networks.filter(
					(o) => o !== net && (o.subnets ?? []).some((s) => overlaps(s, subnet)),
				)
			: [],
	);
	const clashProblem = $derived(networking.problemsOf(nkey)[0]);

	// --- top VMs ---
	let rank = $state<'traffic' | 'started'>('traffic');
	const trafficRes = resource<NetworkTraffic>(
		() => (rank === 'traffic' ? nkey : ''),
		(k) => api.networkTraffic(k, 5),
	);
	const byKeyVM = $derived(new Map(vms.map((v) => [`${v.namespace}/${v.name}`, v])));
	const topTraffic = $derived(
		(trafficRes.data?.vms ?? []).map((t) => ({ t, vm: byKeyVM.get(`${t.namespace}/${t.name}`) })),
	);
	const maxBits = $derived(Math.max(1, ...topTraffic.map((x) => x.t.bitsPerSecond)));
	const recent = $derived(
		vms
			.filter((v) => v.startedAt)
			.sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''))
			.slice(0, 5),
	);
	const mbit = (b: number) =>
		`${b >= 1e9 ? (b / 1e9).toFixed(1) + ' Gbit/s' : Math.round(b / 1e6) + ' Mbit/s'}`;

	// --- change ---
	const secondary = $derived(!!net && net.kind !== 'default');
	let attaching = $state(false);
	let deleting = $state(false);
	const canStage = $derived(
		!!net &&
			(net.scope === 'shared'
				? inventory.canManage
				: !!net.namespace && inventory.namespaces.includes(net.namespace)),
	);
</script>

{#snippet node(cls: string, children: import('svelte').Snippet)}
	<div class="absolute z-[1] flex flex-col gap-0.5 rounded-lg bg-panel {cls}">
		{@render children()}
	</div>
{/snippet}

<div class="flex flex-wrap items-start gap-4">
	<div class="flex min-w-0 flex-[2_1_600px] flex-col gap-4">
		<Card
			title="Where it connects"
			link={{
				label: 'Open in the map',
				href: net ? `/networking?sel=${encodeURIComponent(`net:${nkey}`)}` : '/networking?sel=pod',
			}}
		>
			<div class="overflow-x-auto p-0">
				<div class="relative h-[208px] w-[680px]">
					<span
						class="absolute top-0 left-0 text-[11px] font-bold tracking-[.05em] text-ink-muted uppercase"
						>Way out</span
					>
					{#if throughMain}
						<span class="absolute top-0 left-[64px] text-xs leading-4 text-ink-faint"
							>through its VMs' main network</span
						>
					{/if}
					<span
						class="absolute top-0 left-[496px] text-[11px] font-bold tracking-[.05em] text-ink-muted uppercase"
						>Reaches</span
					>
					<svg
						class="pointer-events-none absolute inset-0 z-0"
						width="680"
						height="208"
						aria-hidden="true"
					>
						{#if via === 'edge'}
							<line x1="56" y1="66" x2="56" y2="86" class="stroke-accent [stroke-width:3]" />
							<line x1="112" y1="98" x2="136" y2="98" class="stroke-accent [stroke-width:3]" />
							<line x1="264" y1="98" x2="288" y2="98" class="stroke-accent [stroke-width:3]" />
						{:else if via === 'fabric'}
							<line x1="112" y1="98" x2="136" y2="98" class="stroke-accent [stroke-width:3]" />
							<line
								x1="264"
								y1="98"
								x2="288"
								y2="98"
								class={vlanUplink?.failures?.length
									? 'stroke-danger [stroke-dasharray:6_4] [stroke-width:2]'
									: 'stroke-accent [stroke-width:3]'}
							/>
						{/if}
						{#each reaches as r, i (i)}
							{@const y = 52 + i * 62}
							<path
								d="M444 {104 + i * 14} C472 {104 + i * 14} 468 {y} 496 {y}"
								fill="none"
								class={r.tone === 'allow'
									? 'stroke-ok [stroke-width:2.5]'
									: r.tone === 'shared'
										? 'stroke-ink-faint [stroke-dasharray:5_4] [stroke-width:2]'
										: 'stroke-wire [stroke-dasharray:1_4] [stroke-linecap:round] [stroke-width:2]'}
							/>
						{/each}
					</svg>

					{#if via === 'edge'}
						{#snippet internet()}
							<span class="flex items-center gap-1.5 text-[13px] leading-4 font-bold"
								><Globe size={14} />Internet</span
							>
						{/snippet}
						{@render node(
							'top-[30px] left-0 h-9 w-28 border-2 border-accent bg-select-soft px-[9px] py-[3px]',
							internet,
						)}
						{#snippet edge()}
							<span class="text-[13px] leading-4 font-bold">Cluster edge</span>
							<span class="text-xs leading-4 text-ink-muted">Source IP is the host's.</span>
						{/snippet}
						{@render node(
							'top-[86px] left-0 h-16 w-28 border-2 border-accent bg-select-soft px-[9px] py-[3px]',
							edge,
						)}
						{#snippet up()}
							<span class="flex items-center gap-1.5 font-mono text-[13px] leading-4 font-medium"
								><Plug size={14} />{builtin ? uplinkLabel(builtin) : 'default'}</span
							>
							<span class="text-xs leading-4 text-ink-muted"
								><span class="font-mono">{builtin?.bridge || 'br-ex'}</span>{builtin?.nodeCount
									? `, ${fmt(builtin.nodeCount)} hosts`
									: ''}</span
							>
						{/snippet}
						{@render node(
							'top-[86px] left-[136px] h-16 w-32 border-2 border-accent bg-select-soft px-[9px] py-[3px]',
							up,
						)}
					{:else if via === 'fabric'}
						{#snippet fabric()}
							<span class="text-[13px] leading-4 font-bold">Datacenter fabric</span>
							<span class="text-xs leading-4 text-ink-muted">VMs keep their IP.</span>
						{/snippet}
						{@render node(
							'top-[86px] left-0 min-h-16 w-28 border-2 border-accent bg-select-soft px-[9px] py-[3px]',
							fabric,
						)}
						{#snippet vup()}
							<span class="flex items-center gap-1.5 font-mono text-[13px] leading-4 font-medium"
								><Plug size={14} />{net?.uplink}</span
							>
							<span
								class="text-xs leading-4 {vlanUplink?.failures?.length
									? 'text-danger-ink'
									: 'text-ink-muted'}"
								>{vlanUplink?.failures?.length
									? `Fails on ${vlanUplink.failures.map((f) => f.node).join(', ')}`
									: vlanUplink
										? `${vlanUplink.bridge}, ${fmt(vlanUplink.nodeCount)} hosts`
										: ''}</span
							>
						{/snippet}
						{@render node(
							`top-[86px] left-[136px] h-16 w-32 border-2 ${vlanUplink?.failures?.length ? 'border-danger/60' : 'border-accent bg-select-soft'} px-[9px] py-[3px]`,
							vup,
						)}
					{/if}
					{#snippet self()}
						<span class="font-mono text-sm leading-5 font-medium">{name}</span>
						{#if subnet}<span class="font-mono text-xs leading-4 text-ink-muted">{subnet}</span
							>{/if}
						<span class="text-xs leading-4 text-ink-muted"
							>{fmt(vms.length)} VMs{project && project !== 'platform'
								? ` in ${project}`
								: ''}</span
						>
					{/snippet}
					{@render node(
						'top-[74px] left-[288px] h-[88px] w-[156px] border-2 border-accent bg-select px-[9px] py-[3px]',
						self,
					)}

					{#each reaches as r, i (i)}
						<div
							class="absolute left-[496px] z-[1] flex min-h-[52px] w-[184px] flex-col gap-0.5 rounded-lg border border-line bg-panel px-2.5 py-1"
							style="top: {26 + i * 62}px"
						>
							<div class="flex min-w-0 items-center gap-1.5">
								{#if r.tone === 'allow'}<ShieldCheck
										size={14}
										class="shrink-0 text-ok"
									/>{:else if r.tone === 'blocked'}<Ban
										size={14}
										class="shrink-0 text-ink-faint"
									/>{/if}
								{#if r.href}
									<a
										href={r.href}
										class="truncate font-mono text-[13px] leading-4 font-medium hover:text-accent-ink"
										>{r.title}</a
									>
								{:else}
									<span
										class="truncate text-[13px] leading-4 {r.tone === 'blocked'
											? 'font-bold'
											: 'font-mono font-medium'}">{r.title}</span
									>
								{/if}
							</div>
							<span class="line-clamp-2 text-xs leading-4 text-ink-muted">{r.sub}</span>
						</div>
					{/each}
					{#if !reaches.length}
						<p class="absolute top-[86px] left-[496px] w-[184px] text-xs text-ink-faint">
							{rules ? 'No rule names another place for it.' : 'Reading the rules...'}
						</p>
					{/if}
				</div>
			</div>
			<div
				class="-mx-card -mb-4 mt-3 flex flex-wrap gap-x-5 gap-y-1.5 border-t border-line-soft px-card py-2.5 text-xs text-ink-soft"
			>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-[3px] border-accent"></span>Way out</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-2 border-ok"></span>Allowed by a rule</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-2 border-dashed border-ink-faint"></span>Some
					VMs on both</span
				>
				<span class="inline-flex items-center gap-1.5"
					><span class="inline-block w-[22px] border-t-2 border-dotted border-wire"></span>Blocked
					by a rule</span
				>
			</div>
		</Card>

		<Card
			title="Rules that apply"
			meta={rules || !net ? `${Math.min(3, lines.length)} of ${lines.length}` : undefined}
			link={lines.length
				? { label: `All ${lines.length} on the Rules tab`, href: '?tab=rules' }
				: undefined}
		>
			{#if rulesFailed}
				<p class="text-[13px] text-danger-ink">The rules could not be read.</p>
			{:else if net && !rules}
				<p class="text-[13px] text-ink-faint">Reading the rules...</p>
			{:else if !lines.length}
				<p class="text-[13px] text-ink-faint">
					No rule bears on it. Its VMs accept traffic from anywhere.
				</p>
			{:else}
				<RuleList lines={topLines(lines, 3)} owner gap="gap-3" />
			{/if}
		</Card>

		<Card
			title="Recent changes"
			link={net?.sourceFile ? { label: 'History tab', href: '?tab=history' } : undefined}
		>
			{#if !net}
				<p class="text-[13px] text-ink-faint">Built in: git holds nothing for it.</p>
			{:else if !net.sourceFile}
				<p class="text-[13px] text-ink-faint">Not in git, so nothing records its changes.</p>
			{:else if histRes.failed}
				<p class="text-[13px] text-danger-ink">Its history could not be read.</p>
			{:else if !histRes.data}
				<p class="text-[13px] text-ink-faint">Reading its history...</p>
			{:else if !histRes.data.length}
				<p class="text-[13px] text-ink-faint">No merged change yet.</p>
			{:else}
				<ul class="flex flex-col gap-3">
					{#each histRes.data.slice(0, 2) as c (c.hash)}
						<li class="grid grid-cols-[16px_minmax(0,1fr)] gap-x-2.5 text-[13px]">
							<GitMerge size={16} class="mt-0.5 text-accent-ink" />
							{#if c.prURL}
								<a
									href={c.prURL}
									target="_blank"
									rel="noreferrer"
									class="text-accent-ink hover:underline"
									><span class="font-mono">#{c.prNumber}</span> {c.title}</a
								>
							{:else}
								<span>{c.title}</span>
							{/if}
							<span class="col-start-2 text-xs leading-4 text-ink-faint"
								>Merged {mergedWhen(c.when)} by {c.author}</span
							>
						</li>
					{/each}
				</ul>
			{/if}
		</Card>
	</div>

	<div class="flex min-w-0 flex-[1_1_320px] flex-col gap-4">
		<Card title="Address use">
			{#snippet actions()}
				{#if subnet}<span class="font-mono text-ink-faint">{subnet}</span>{/if}
			{/snippet}
			{#if subnet && capacity}
				<div class="flex flex-col gap-2.5">
					<div class="flex flex-wrap items-baseline gap-2">
						<span class="text-[28px] leading-9 font-bold">{fmt(used)}</span>
						<span class="text-ink-soft">of {fmt(capacity)} used</span>
					</div>
					<div
						role="img"
						aria-label="{fmt(used)} of {fmt(capacity)} addresses used"
						class="h-2 overflow-hidden rounded bg-inset-strong"
					>
						<div
							class="h-2 min-w-[3px] {used / capacity > 0.9
								? 'bg-danger'
								: used / capacity > 0.75
									? 'bg-warn'
									: 'bg-present'}"
							style="width: {Math.min(100, (used / capacity) * 100)}%"
						></div>
					</div>
					<span class="text-[13px] text-ink-faint">
						Room for {fmt(Math.max(0, capacity - used))} more.
						{#if !clash.length}Overlaps no other network.{/if}
					</span>
					{#if clashProblem}
						<div
							class="rounded-md border border-warn/60 bg-warn-soft px-3 py-2 text-[13px] text-warn-ink"
						>
							{clashProblem.detail}
							{#each clash.slice(0, 2) as o (networkKey(o))}
								<a
									href={networkHref(networkKey(o))}
									class="ml-1 font-mono text-accent-ink underline underline-offset-2">{o.name}</a
								>
							{/each}
						</div>
					{:else if clash.length}
						<span class="text-[13px] text-ink-faint">
							{clash.map((o) => o.name).join(', ')}
							{clash.length === 1 ? 'reuses' : 'reuse'} this range in another namespace. They never meet,
							so that is fine.
						</span>
					{/if}
				</div>
			{:else}
				<p class="text-[13px] text-ink-faint">
					{net
						? net.kind === 'vlan'
							? 'The datacenter fabric hands out its addresses: no range is managed here.'
							: 'No address range: its VMs set their own.'
						: 'Each host hands out pod addresses from the cluster range.'}
				</p>
			{/if}
		</Card>

		{#if net}
			<Card title="Change {net.name}">
				{#snippet actions()}
					<span class="text-ink-faint">Stages in {project}</span>
				{/snippet}
				<div class="-my-3 flex flex-col">
					{#if secondary}
						<div class="flex flex-col gap-1.5 border-b border-line-soft py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button
									variant="outline"
									size="sm"
									disabled={!canStage}
									onclick={() => (attaching = true)}><Plus size={14} />Attach VMs</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="info"
								>Each VM gets a {net.name} NIC at its next restart.</Consequence
							>
						</div>
					{/if}
					{#if subnet}
						<div class="flex flex-col gap-1.5 border-b border-line-soft py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" size="sm" disabled>Change subnet</Button>
							</div>
							<Consequence size="sm"
								>A subnet is fixed once created: a new range means a new network.</Consequence
							>
						</div>
					{/if}
					{#if canEditNetwork(net)}
						<div class="flex flex-col gap-1.5 border-b border-line-soft py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" size="sm" onclick={() => openEdit(networkRef(net))}
									>Change who can attach</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="warn"
								>Projects taken off lose it; their VMs on it fail to start until detached.</Consequence
							>
						</div>
					{/if}
					{#if canDeleteNetwork(net)}
						<div class="flex flex-col gap-1.5 py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button
									variant="danger-secondary"
									size="sm"
									disabled={!secondary && vms.length > 0}
									onclick={() =>
										secondary ? (deleting = true) : openDelete(networkRef(net), net.sourceFile!)}
									>Delete</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="danger">
								{#if !vms.length}Removes it. No VM uses it.{:else if secondary}Detaches all {fmt(
										vms.length,
									)} VMs in the same pull request.{:else}{fmt(vms.length)} VMs use it as their main network.
									Move them first.{/if}
							</Consequence>
						</div>
					{/if}
					{#if canAdoptNetwork(net)}
						<div class="flex flex-col gap-1.5 py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" size="sm" onclick={() => openAdopt(networkRef(net))}
									>Adopt into git</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="info"
								>Stages its running manifest in {project}; then it changes here like any other.</Consequence
							>
						</div>
					{:else if net.sourceFile && net.sync === 'OutOfSync' && canAuthorRef(networkRef(net))}
						<div class="flex flex-col gap-1.5 py-3">
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" size="sm" onclick={() => openAdopt(networkRef(net), true)}
									>Adopt live changes</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="warn"
								>The cluster differs from git; this brings git up to what runs.</Consequence
							>
						</div>
					{/if}
					{#if !secondary && net.sourceFile && !canEditNetwork(net) && !canDeleteNetwork(net)}
						<p class="py-3 text-[13px] text-ink-faint">
							{net.kind === 'default'
								? `${project}'s main network lives as long as its namespace.`
								: 'You cannot stage changes to it.'}
						</p>
					{/if}
				</div>
			</Card>
		{/if}

		<Card
			title="Top VMs"
			meta="{Math.min(5, vms.length)} of {fmt(vms.length)}"
			flush
			link={vms.length
				? { label: `All ${fmt(vms.length)} on the VMs tab`, href: '?tab=vms' }
				: undefined}
		>
			<div class="px-card py-3">
				<div
					role="group"
					aria-label="Rank by"
					class="inline-flex overflow-hidden rounded-md border border-line text-[13px]"
				>
					<button
						type="button"
						aria-pressed={rank === 'traffic'}
						onclick={() => (rank = 'traffic')}
						class="px-2.5 py-1 {rank === 'traffic'
							? 'bg-select text-accent-ink'
							: 'bg-panel text-ink-soft hover:bg-inset'}">Traffic, last hour</button
					>
					<button
						type="button"
						aria-pressed={rank === 'started'}
						onclick={() => (rank = 'started')}
						class="border-l border-line px-2.5 py-1 {rank === 'started'
							? 'bg-select text-accent-ink'
							: 'bg-panel text-ink-soft hover:bg-inset'}">Recently started</button
					>
				</div>
			</div>
			{#if rank === 'traffic' && trafficRes.failed}
				<p class="px-card pb-4 text-[13px] text-ink-faint">
					Traffic needs the metrics backend. Rank by recently started instead.
				</p>
			{:else if rank === 'traffic' && !trafficRes.data}
				<p class="px-card pb-4 text-[13px] text-ink-faint">Reading traffic...</p>
			{:else}
				<table class="w-full text-[13px]">
					<thead>
						<tr class="border-y border-line-soft text-left text-xs text-ink-faint">
							<th class="py-2 pl-card font-bold text-ink">VM</th>
							<th class="py-2 font-bold text-ink">Host</th>
							<th class="py-2 pr-card text-right font-bold text-ink"
								>{rank === 'traffic' ? 'Traffic' : 'Started'}</th
							>
						</tr>
					</thead>
					<tbody>
						{#if rank === 'traffic'}
							{#each topTraffic as { t, vm } (`${t.namespace}/${t.name}`)}
								<tr class="border-b border-line-soft last:border-b-0">
									<td class="py-2 pl-card"
										><a
											href={vmHref(t.namespace, t.name)}
											class="font-mono text-accent-ink hover:underline">{t.name}</a
										></td
									>
									<td class="py-2 text-ink-soft">{vm?.nodeName ?? '-'}</td>
									<td class="py-2 pr-card text-right whitespace-nowrap">
										<span class="mr-2 inline-block h-1.5 w-14 rounded bg-inset-strong align-middle"
											><span
												class="block h-1.5 rounded bg-present"
												style="width: {(t.bitsPerSecond / maxBits) * 100}%"
											></span></span
										>{mbit(t.bitsPerSecond)}
									</td>
								</tr>
							{:else}
								<tr
									><td colspan="3" class="px-card py-3 text-ink-faint"
										>No traffic in the last hour.</td
									></tr
								>
							{/each}
						{:else}
							{#each recent as v (`${v.namespace}/${v.name}`)}
								<tr class="border-b border-line-soft last:border-b-0">
									<td class="py-2 pl-card"
										><a
											href={vmHref(v.namespace, v.name)}
											class="font-mono text-accent-ink hover:underline">{v.name}</a
										></td
									>
									<td class="py-2 text-ink-soft">{v.nodeName ?? '-'}</td>
									<td class="py-2 pr-card text-right text-ink-soft">{relativeAge(v.startedAt)}</td>
								</tr>
							{:else}
								<tr><td colspan="3" class="px-card py-3 text-ink-faint">No VM is running.</td></tr>
							{/each}
						{/if}
					</tbody>
				</table>
			{/if}
		</Card>
	</div>
</div>

{#if attaching && net}
	<AttachVMsModal {net} onclose={() => (attaching = false)} />
{/if}
{#if deleting && net}
	<DeleteNetworkModal {net} {vms} onclose={() => (deleting = false)} />
{/if}
