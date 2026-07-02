<script lang="ts">
	import type { Network, PhysicalAdapter, Project, Uplink, VM } from '$lib/api';
	import { POD_NETWORK, type Scope } from '$lib/lenses';
	import { networkByRef, kindLabel } from '$lib/networks';
	import InfoCard from './InfoCard.svelte';
	import NodeActions from './NodeActions.svelte';
	import QuotaBand from './QuotaBand.svelte';
	import Row from './Row.svelte';

	// The container Configure tab: read-only settings for whatever the tree has
	// focused. dotvirt owns nothing here — projects are namespace labels, config
	// is the repo; node/storage facts come from the cluster platform.
	let {
		scope,
		vms,
		projects,
		networks = [],
		uplinks = [],
		physicalAdapters = [],
		nmstatePresent = false,
		canManage = false,
		onaction,
		onadduplink
	}: {
		scope: Scope;
		vms: VM[]; // the VMs in scope (counts + the node evacuate list)
		projects: Project[]; // the scoped project(s) — empty for node/network/storage
		networks?: Network[];
		uplinks?: Uplink[];
		physicalAdapters?: PhysicalAdapter[];
		nmstatePresent?: boolean;
		canManage?: boolean;
		onaction?: (a: { verb: string; namespace: string; name: string; ok: boolean }) => void;
		onadduplink?: () => void;
	} = $props();
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		{#if scope.kind === 'network'}
			{@const pg = networkByRef(scope.network, networks)}
			<InfoCard title={pg ? pg.name : scope.network}>
				<dl class="divide-y divide-slate-100 text-[13px]">
					<Row
						label="Type"
						value={pg
							? kindLabel(pg.kind)
							: scope.network === POD_NETWORK
								? 'Pod network (cluster default)'
								: ''}
					/>
					{#if pg}
						<Row
							label="Scope"
							value={pg.scope === 'shared' ? 'Shared · all projects' : `Project · ${pg.namespace}`}
						/>
						{#if pg.vlan}<Row label="VLAN" value={String(pg.vlan)} />{/if}
						{#if pg.uplink}<Row label="Uplink" value={pg.uplink} />{/if}
						{#if pg.subnets?.length}<Row label="Subnets" value={pg.subnets.join(', ')} />{/if}
						<Row label="Backing">
							<span class="text-slate-500">{pg.backing}</span>
						</Row>
					{/if}
					<Row label="VMs attached" value={String(vms.length)} />
				</dl>
			</InfoCard>
		{:else if scope.kind === 'node' || scope.kind === 'storage'}
			<InfoCard
				title={scope.kind === 'node'
					? `Node: ${scope.node}`
					: `Storage class: ${scope.storageClass}`}
			>
				<dl class="divide-y divide-slate-100 text-[13px]">
					<Row
						label={scope.kind === 'node' ? 'VMs placed here' : 'VMs attached'}
						value={String(vms.length)}
					/>
				</dl>
				<p class="border-t border-slate-100 px-3 py-2 text-xs text-slate-400">
					{scope.kind === 'node'
						? 'Node configuration is managed by the cluster platform, not dotvirt.'
						: 'Storage classes are managed by the cluster platform, not dotvirt.'}
				</p>
			</InfoCard>
			{#if scope.kind === 'node'}
				{@const nodeName = scope.node}
				<!-- Node maintenance-lite: cordon/uncordon + evacuate (shown only
			     when the caller's token may patch nodes). -->
				<NodeActions node={scope.node} {vms} {onaction} />
				{#if uplinks.length}
					<InfoCard title="Uplinks">
						{#snippet action()}
							<button
								onclick={() => onadduplink?.()}
								disabled={!canManage}
								title={canManage ? '' : 'Requires platform-network authoring permission'}
								class="text-xs text-blue-600 hover:underline disabled:text-slate-300"
								>+ Add uplink</button
							>
						{/snippet}
						<ul class="divide-y divide-slate-100 px-3 text-[13px]">
							{#each uplinks.filter((u) => !u.nodes || u.nodes.includes(nodeName)) as u (u.name)}
								<li class="flex items-baseline justify-between gap-3 py-1.5">
									<span class="text-slate-800">{u.name}{u.builtin ? ' · default' : ''}</span>
									<span class="text-slate-400"
										>{u.bridge} · {u.nodeCount} node{u.nodeCount === 1 ? '' : 's'}</span
									>
								</li>
							{/each}
						</ul>
					</InfoCard>
				{/if}
				<InfoCard title="Physical adapters">
					{#if !nmstatePresent}
						<p class="px-3 py-3 text-xs text-slate-400">
							Install the NMState operator instance to discover physical adapters.
						</p>
					{:else}
						{@const nics = physicalAdapters.filter((a) => a.node === nodeName)}
						{#if nics.length}
							<ul class="divide-y divide-slate-100 px-3 text-[13px]">
								{#each nics as a (a.name)}
									<li class="flex items-baseline justify-between gap-3 py-1.5">
										<span class="text-slate-800">{a.name}</span>
										<span class="flex items-center gap-3 text-right text-slate-400">
											<span>{a.role}</span>
											<span>{a.state}{a.mtu ? ` · MTU ${a.mtu}` : ''}</span>
										</span>
									</li>
								{/each}
							</ul>
						{:else}
							<p class="px-3 py-3 text-xs text-slate-400">No physical adapters reported.</p>
						{/if}
					{/if}
				</InfoCard>
			{/if}
		{:else}
			{#each projects as p (p.name)}
				<InfoCard title="Project: {p.name}">
					<dl class="divide-y divide-slate-100 text-[13px]">
						<Row label="Repository">
							{#if p.repo}
								<a
									href={p.repo}
									target="_blank"
									class="font-mono text-xs text-blue-600 hover:underline">{p.repo}</a
								>
							{:else}
								<span class="text-slate-400">— not configured</span>
							{/if}
						</Row>
						<Row label="Namespaces">
							{#each p.namespaces as n (n.namespace)}
								<span
									class="ml-1 inline-block rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-600"
									>{n.namespace} · {n.vms.length} VMs</span
								>
							{/each}
						</Row>
					</dl>
					{#if p.error}
						<p class="border-t border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700">
							{p.error}
						</p>
					{/if}
					<!-- Quota-aware capacity: the project's ResourceQuotas. -->
					<div class="border-t border-slate-100 px-3 py-2">
						<QuotaBand scope={{ project: p.name }} showEmpty />
					</div>
				</InfoCard>
			{/each}
		{/if}
	</div>
</div>
