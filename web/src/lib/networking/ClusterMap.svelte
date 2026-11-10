<script lang="ts">
	import { ChevronDown, CircleX, Globe, Plug, Search, TriangleAlert } from '@lucide/svelte';
	import type { VM } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { POD_NETWORK } from '#lib/lenses.js';
	import type { ClusterMap, NetNode, NetProblem, UplinkRow, WayOut } from '#lib/netmap.js';
	import { canEditUplink, openEdit, uplinkRef } from '#lib/objects.js';
	import { networkHref } from '#lib/nav.js';
	import Button from '#lib/components/Button.svelte';
	import StageChip from '#lib/components/StageChip.svelte';

	// The cluster map: four lanes (outside, uplinks, shared and VLAN networks,
	// projects) as placed HTML nodes, with the connectors drawn in one SVG layer
	// underneath from the nodes' measured boxes, so content can grow without
	// hand-placed coordinates. Selection, the way out and problems come in as
	// data; the map only draws them.
	let {
		model,
		sel,
		way,
		hit,
		zoom = 1,
		onselect,
		onmorenets,
		onmoreprojects,
	}: {
		model: ClusterMap;
		sel: string;
		way: WayOut | null;
		hit: { vm: VM; project: string } | null;
		zoom?: number;
		onselect: (sel: string) => void;
		onmorenets: () => void;
		onmoreprojects: () => void;
	} = $props();

	const builtin = $derived(model.rows.find((r) => r.uplink.builtin)!);
	const vlanRows = $derived(model.rows.filter((r) => !r.uplink.builtin));
	const fmt = (n: number) => n.toLocaleString('en-US');

	// The nodes the way out runs through, lit blue.
	const onPath = $derived.by(() => {
		const s = new Set<string>();
		if (way?.via === 'edge') {
			s.add('internet').add('edge').add(`up:${builtin.uplink.name}`);
			if (way.network === POD_NETWORK) s.add('pod');
		} else if (way?.via === 'fabric') {
			s.add('fabric').add(`up:${way.uplink}`).add(`net:${way.network}`);
		}
		if (hit && model.projects.some((p) => p.name === hit.project)) s.add(`proj:${hit.project}`);
		return s;
	});
	const hitInMore = $derived(!!hit && !model.projects.some((p) => p.name === hit.project));
	const isOn = (id: string) => sel === id || onPath.has(id);

	// --- connectors, from measured node boxes ---
	let root = $state<HTMLDivElement>();
	let boxes = $state<Record<string, { x: number; y: number; w: number; h: number }>>({});
	let size = $state({ w: 744, h: 600 });

	function measure() {
		if (!root) return;
		const out: typeof boxes = {};
		for (const el of root.querySelectorAll<HTMLElement>('[data-node]'))
			out[el.dataset.node!] = {
				x: el.offsetLeft,
				y: el.offsetTop,
				w: el.offsetWidth,
				h: el.offsetHeight,
			};
		boxes = out;
		size = { w: root.offsetWidth, h: root.offsetHeight };
	}
	$effect(() => {
		void model;
		void sel;
		void hit;
		measure();
	});
	$effect(() => {
		if (!root) return;
		const ro = new ResizeObserver(measure);
		ro.observe(root);
		return () => ro.disconnect();
	});

	type Line = {
		x1: number;
		y1: number;
		x2: number;
		y2: number;
		kind: 'attached' | 'failing' | 'path';
	};
	const lines = $derived.by(() => {
		const b = boxes;
		const out: Line[] = [];
		const h = (from: string, to: string, kind: Line['kind']) => {
			const a = b[from];
			const c = b[to];
			if (!a || !c) return;
			const y = Math.max(a.y, c.y) + 12;
			out.push({ x1: a.x + a.w, y1: y, x2: c.x, y2: y, kind });
		};
		const internet = b.internet;
		const edge = b.edge;
		const viaEdge = way?.via === 'edge';
		if (internet && edge) {
			const x = internet.x + Math.round(internet.w / 2) - 1;
			out.push({
				x1: x,
				y1: internet.y + internet.h,
				x2: x,
				y2: edge.y,
				kind: viaEdge ? 'path' : 'attached',
			});
		}
		h('edge', `up:${builtin.uplink.name}`, viaEdge ? 'path' : 'attached');
		for (const r of vlanRows)
			h(
				'fabric',
				`up:${r.uplink.name}`,
				r.problem
					? 'failing'
					: way?.via === 'fabric' && way.uplink === r.uplink.name
						? 'path'
						: 'attached',
			);
		h(
			`up:${builtin.uplink.name}`,
			'pod',
			viaEdge && way?.network === POD_NETWORK ? 'path' : 'attached',
		);
		for (const r of model.rows)
			for (const n of r.nets)
				h(
					`up:${r.uplink.name}`,
					`net:${n.key}`,
					n.cutOff.length
						? 'failing'
						: way?.via === 'fabric' && way.network === n.key
							? 'path'
							: 'attached',
				);

		// The projects hang off a bus from the pod network: every project
		// reaches the default uplink through it.
		const pod = b.pod;
		const targets = [...model.projects.map((p) => `proj:${p.name}`), 'more-proj'].filter(
			(k) => b[k],
		);
		if (pod && targets.length) {
			const busX = pod.x + pod.w + 16;
			const y0 = pod.y + 12;
			const yEnd = b[targets[targets.length - 1]].y + 12;
			out.push({
				x1: pod.x + pod.w,
				y1: y0,
				x2: busX,
				y2: y0,
				kind: viaEdge && hit ? 'path' : 'attached',
			});
			out.push({ x1: busX, y1: y0, x2: busX, y2: yEnd, kind: 'attached' });
			const hitKey = hit ? (hitInMore ? 'more-proj' : `proj:${hit.project}`) : '';
			for (const k of targets) {
				const y = b[k].y + 12;
				const lit = viaEdge && k === hitKey;
				if (lit) out.push({ x1: busX, y1: y0, x2: busX, y2: y, kind: 'path' });
				out.push({ x1: busX, y1: y, x2: b[k].x, y2: y, kind: lit ? 'path' : 'attached' });
			}
		}
		return out;
	});

	const STROKE = {
		attached: 'stroke-wire [stroke-width:2]',
		failing: 'stroke-danger [stroke-width:2] [stroke-dasharray:6_4]',
		path: 'stroke-accent [stroke-width:3]',
	};

	const nodeClass = (id: string, extra = '') =>
		`relative z-[1] flex min-w-0 flex-col gap-0.5 rounded-lg bg-panel text-left ${
			isOn(id)
				? 'border-2 border-accent bg-select-soft px-[9px] py-[3px]'
				: `border px-2.5 py-1 ${extra.includes('bad') ? 'border-danger/40' : extra.includes('agg') ? 'border-dashed border-ink-faint/60' : 'border-line'}`
		}`;
	const hostsText = (r: UplinkRow) =>
		!r.known
			? 'Details need host access'
			: r.allHosts && r.uplink.nodeCount > 1
				? `all ${fmt(r.uplink.nodeCount)} hosts`
				: plural(r.uplink.nodeCount, 'host');
	const netSub = (n: NetNode) =>
		n.net.kind === 'vlan'
			? `VLAN ${n.net.vlan ? `${n.net.vlan} ` : ''}on ${n.net.uplink ?? 'an uplink'}`
			: 'Shared';
</script>

{#snippet badge(tone: 'danger' | 'warn', text: string)}
	<span
		class="inline-flex h-5 items-center gap-1 self-start rounded-full border px-2 text-[11px] leading-[18px] font-medium whitespace-nowrap {tone ===
		'danger'
			? 'border-danger/40 bg-danger-soft text-danger-ink'
			: 'border-warn/50 bg-warn-soft text-warn-ink'}"
	>
		{#if tone === 'warn'}<TriangleAlert size={12} />{/if}{text}
	</span>
{/snippet}

{#snippet name(id: string, label: string, mono = true)}
	<button
		type="button"
		onclick={() => onselect(id)}
		class="min-w-0 text-left text-[13px] leading-4 text-ink hover:text-accent-ink {mono
			? 'truncate font-mono font-medium'
			: 'font-bold'}">{label}</button
	>
{/snippet}

{#snippet problemFix(p: NetProblem)}
	{@render badge('warn', p.title)}
	<span class="text-xs leading-4 text-ink-muted">{p.detail}</span>
	{#if p.networks?.length}
		<a
			href={networkHref(p.networks[0])}
			class="text-xs leading-4 font-medium text-accent-ink hover:underline">Fix the overlap</a
		>
	{/if}
{/snippet}

{#snippet uplinkNode(r: UplinkRow)}
	{@const id = `up:${r.uplink.name}`}
	{@const hostHere = way && way.uplink === r.uplink.name && way.host && hit}
	<div data-node={id} class="{nodeClass(id, r.problem ? 'bad' : '')} h-full">
		<span class="flex items-center gap-1.5 {r.problem ? 'text-danger-ink' : 'text-ink'}">
			{#if r.problem}<CircleX size={14} class="shrink-0" />{:else}<Plug
					size={14}
					class="shrink-0"
				/>{/if}
			{@render name(id, r.label)}
		</span>
		<span class="text-xs leading-4 text-ink-muted">
			{#if r.uplink.bridge}<span class="font-mono text-xs">{r.uplink.bridge}</span>,{/if}<span
				class={r.uplink.bridge ? 'ml-1' : ''}>{hostsText(r)}</span
			>
		</span>
		{#if r.uplink.builtin}
			<span class="text-xs leading-4 text-ink-muted"
				>Pod, Shared and project networks leave here.</span
			>
		{/if}
		{#if r.problem}
			{@render badge('danger', r.problem.title)}
			<span class="font-mono text-[11px] leading-4 text-danger-ink">{r.problem.reason}</span>
			{#if canEditUplink(r.uplink)}
				<div class="mt-1.5 flex flex-wrap items-center gap-1.5">
					<Button variant="outline" size="sm" onclick={() => openEdit(uplinkRef(r.uplink))}
						>Edit {r.label}</Button
					>
					<StageChip kind="pr" />
				</div>
				<span class="text-xs leading-4 text-ink-faint"
					>Merging a fix retries it on {r.problem.hosts?.length === 1
						? r.problem.hosts[0]
						: 'those hosts'}.</span
				>
			{:else}
				<a href="/networking?tab=uplinks" class="text-xs leading-4 text-accent-ink hover:underline"
					>Open on the Uplinks tab</a
				>
			{/if}
		{/if}
		{#if hostHere}
			<span
				class="mt-auto flex items-center gap-1.5 rounded bg-accent px-1.5 py-0.5 text-xs leading-4 text-white"
			>
				<span class="font-mono">{way!.host}</span><span class="ml-auto">{hit!.vm.name} here</span>
			</span>
		{/if}
	</div>
{/snippet}

{#snippet netNode(n: NetNode)}
	{@const id = `net:${n.key}`}
	<div data-node={id} class={nodeClass(id, n.cutOff.length ? 'bad' : '')}>
		<div class="flex min-w-0 items-center gap-1.5">
			{@render name(id, n.net.name)}
			<span class="ml-auto text-xs leading-4 font-bold text-ink">{fmt(n.vms)}</span>
		</div>
		<span class="truncate text-xs leading-4 text-ink-muted">
			{netSub(n)}{#if n.net.kind !== 'vlan' && n.net.subnets?.length},
				<span class="font-mono">{n.net.subnets[0]}</span>{/if}
		</span>
		{#if n.cutOff.length}
			{@render badge(
				'danger',
				`Cut off on ${n.cutOff.length === 1 ? n.cutOff[0] : `${n.cutOff.length} hosts`}`,
			)}
		{/if}
		{#each n.problems as p (p.id)}{@render badge(p.severity, p.title)}{/each}
	</div>
{/snippet}

<div class="relative" style="width: {size.w * zoom}px; height: {size.h * zoom}px">
	<div
		bind:this={root}
		class="absolute top-0 left-0 w-[744px] origin-top-left"
		style="transform: scale({zoom})"
	>
		<svg
			class="pointer-events-none absolute inset-0 z-0"
			width={size.w}
			height={size.h}
			aria-hidden="true"
		>
			{#each lines as l, i (i)}
				<line x1={l.x1} y1={l.y1} x2={l.x2} y2={l.y2} class={STROKE[l.kind]} />
			{/each}
		</svg>

		<div class="flex items-start gap-8">
			<div class="grid w-[514px] shrink-0 grid-cols-[128px_162px_176px] gap-x-6 gap-y-3">
				<!-- lane titles -->
				{@render lane('Outside', vlanRows.length ? '2 ways out' : '1 way out')}
				{@render lane('Uplinks', `All ${model.rows.length}`)}
				{@render lane(
					'Shared and VLAN networks',
					model.shown.lane === model.shown.laneTotal
						? `All ${model.shown.laneTotal}`
						: `${model.shown.lane} most used of ${fmt(model.shown.laneTotal)}`,
				)}

				<!-- the builtin uplink's row: internet and the cluster edge -->
				<div class="flex flex-col gap-4">
					<div data-node="internet" class={nodeClass('internet')}>
						<span class="flex items-center gap-1.5 text-[13px] leading-4 font-bold text-ink"
							><Globe size={14} />Internet</span
						>
					</div>
					<div data-node="edge" class={nodeClass('edge')}>
						{@render name('edge', 'Cluster edge', false)}
						<span class="text-xs leading-4 text-ink-muted">Source IP is the host's.</span>
						{#if model.edge.fixedIPs}
							<a
								href="/networking?tab=firewall"
								class="text-xs leading-4 text-accent-ink hover:underline"
								>{model.edge.fixedIPs} fixed outbound IP{model.edge.fixedIPs === 1 ? '' : 's'}</a
							>
						{/if}
						{#if model.edge.routes}
							<a
								href="/networking?tab=firewall"
								class="text-xs leading-4 text-accent-ink hover:underline"
								>{model.edge.routes} external route{model.edge.routes === 1 ? '' : 's'}</a
							>
						{/if}
					</div>
				</div>
				{@render uplinkNode(builtin)}
				<div class="flex flex-col gap-2">
					<div data-node="pod" class={nodeClass('pod')}>
						{@render name('pod', 'Pod network', false)}
						<span class="text-xs leading-4 text-ink-muted">Built in, every project.</span>
						<span class="text-xs leading-4 text-ink-muted"
							>{plural(model.podOnly, 'VM')} {model.podOnly === 1 ? 'uses' : 'use'} only this.</span
						>
					</div>
					{#each builtin.nets as n (n.key)}{@render netNode(n)}{/each}
				</div>

				<!-- the VLAN uplinks' rows, beside the datacenter fabric -->
				{#each vlanRows as r, i (r.uplink.name)}
					{#if i === 0}
						<div
							data-node="fabric"
							class="{nodeClass('fabric')} mt-3"
							style="grid-row: span {vlanRows.length}"
						>
							{@render name('fabric', 'Datacenter fabric', false)}
							<span class="text-xs leading-4 text-ink-muted">Physical switches and VLANs.</span>
							<span class="mt-auto text-xs leading-4 text-ink-muted"
								>VLAN networks skip the cluster edge. VMs keep their own IP.</span
							>
						</div>
					{/if}
					<div class={i === 0 ? 'mt-3' : ''}>{@render uplinkNode(r)}</div>
					<div class="flex flex-col gap-2 {i === 0 ? 'mt-3' : ''}">
						{#each r.nets as n (n.key)}{@render netNode(n)}{/each}
					</div>
				{/each}

				{#if model.moreNetworks}
					<div class="col-start-3">
						<div data-node="more-nets" class={nodeClass('more-nets', 'agg')}>
							<button
								type="button"
								onclick={onmorenets}
								aria-expanded="false"
								class="flex items-center gap-1 self-start text-[13px] leading-4 font-bold text-accent-ink hover:underline"
								>{fmt(model.moreNetworks.count)} more network{model.moreNetworks.count === 1
									? ''
									: 's'}<ChevronDown size={14} /></button
							>
							<span class="text-xs leading-4 text-ink-muted"
								>{[
									model.moreNetworks.shared ? `${model.moreNetworks.shared} Shared` : '',
									model.moreNetworks.vlan ? `${model.moreNetworks.vlan} VLAN` : '',
								]
									.filter(Boolean)
									.join(', ')}</span
							>
						</div>
					</div>
				{/if}
			</div>

			<!-- projects -->
			<div class="flex w-[198px] shrink-0 flex-col gap-4">
				{@render lane(
					'Projects',
					model.shown.projects === model.shown.projectsTotal
						? `All ${model.shown.projectsTotal}`
						: `${model.shown.projects} biggest of ${fmt(model.shown.projectsTotal)}`,
				)}
				{#each model.projects as p (p.name)}
					{@const id = `proj:${p.name}`}
					<div data-node={id} class={nodeClass(id)}>
						<div class="flex min-w-0 items-center gap-1.5">
							{@render name(id, p.name)}
							<span class="ml-auto text-xs leading-4 font-bold text-ink">{fmt(p.vms)}</span>
						</div>
						<span class="text-xs leading-4 text-ink-muted"
							>{p.networks} network{p.networks === 1 ? '' : 's'}</span
						>
						{#each p.problems as pr (pr.id)}{@render problemFix(pr)}{/each}
						{#if hit && hit.project === p.name}{@render hitChip()}{/if}
					</div>
				{/each}
				{#if model.moreProjects}
					{@const m = model.moreProjects}
					<div data-node="more-proj" class={nodeClass('more-proj', 'agg')}>
						<div class="flex min-w-0 items-center gap-1.5">
							<button
								type="button"
								onclick={onmoreprojects}
								aria-expanded="false"
								class="flex items-center gap-1 text-[13px] leading-4 font-bold text-accent-ink hover:underline"
								>{fmt(m.count)} more project{m.count === 1 ? '' : 's'}<ChevronDown
									size={12}
								/></button
							>
							<span class="ml-auto text-xs leading-4 font-bold text-ink">{fmt(m.vms)}</span>
						</div>
						<span class="text-xs leading-4 text-ink-muted"
							>{fmt(m.networks)} network{m.networks === 1 ? '' : 's'}</span
						>
						{#each m.problems as pr (pr.id)}<div class="mt-1 flex flex-col gap-0.5">
								{@render problemFix(pr)}
							</div>{/each}
						{#if hitInMore}{@render hitChip()}{/if}
					</div>
				{/if}
			</div>
		</div>
	</div>
</div>

{#snippet lane(title: string, sub: string)}
	<div class="flex h-8 flex-col">
		<span
			class="text-[11px] leading-4 font-bold tracking-[.05em] whitespace-nowrap text-ink-muted uppercase"
			>{title}</span
		>
		<span class="text-xs leading-4 whitespace-nowrap text-ink-faint">{sub}</span>
	</div>
{/snippet}

{#snippet hitChip()}
	<span
		class="mt-1 flex items-center gap-1.5 rounded bg-accent px-1.5 py-0.5 text-xs leading-4 text-white"
	>
		<Search size={12} /><span class="font-mono">{hit!.vm.name}</span><span
			>in {hit!.vm.namespace}</span
		>
	</span>
{/snippet}
