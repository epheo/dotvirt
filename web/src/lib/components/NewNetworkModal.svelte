<script lang="ts">
	import { api, type NetworkCreate, type Uplink } from '$lib/api';
	import { TERMS, dual } from '$lib/vocab';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	let {
		namespaces,
		uplinks = [],
		canManage = false,
		onclose,
		onstaged,
		onAddUplink,
	}: {
		namespaces: string[];
		uplinks?: Uplink[]; // discovered Tier-0 uplinks (physical-network hints for a VLAN segment)
		canManage?: boolean; // caller may author platform-tier segments (shared CUDN / VLAN localnet)
		onclose: () => void;
		onstaged: () => void;
		onAddUplink?: () => void; // open the Add Uplink (Tier-0 transport) wizard from the VLAN flow
	} = $props();

	// A segment is either an overlay (Geneve) Layer2 network — project-scoped (UDN) or
	// shared across projects (CUDN) — or a VLAN segment bridged to a Tier-0 uplink
	// (localnet CUDN). The primary "VM Network" is NOT created here: it is a Tier-1
	// segment born with its namespace, so it lives in New Namespace / New Project.
	let kind = $state<'overlay' | 'vlan'>('overlay');
	let name = $state('');
	let subnet = $state('');
	let namespace = $state(''); // overlay · this project (a namespace-scoped UDN)
	let shared = $state(false); // overlay: false = this project (UDN), true = shared across projects (CUDN)
	let vlan = $state<number | undefined>(undefined);
	let physnet = $state('');
	let selectedNs = $state<string[]>([]);

	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = namespaces[0] ?? '';
	});

	// A cluster-scoped (platform-tier) segment — a shared overlay or any VLAN — carries
	// a namespace multiselect: the set of projects it is published to. Mirrors the
	// backend routing cluster-scoped creates to the platform repo.
	const isShared = $derived(kind === 'vlan' || shared);

	const valid = $derived(
		kind === 'vlan'
			? !!(name && physnet.trim() && vlan && selectedNs.length)
			: shared
				? !!(name && selectedNs.length)
				: !!(name && namespace),
	);

	function toggleNs(ns: string, on: boolean) {
		selectedNs = on ? [...selectedNs, ns] : selectedNs.filter((n) => n !== ns);
	}

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		try {
			const req: NetworkCreate =
				kind === 'vlan'
					? { name, scope: 'vlan', physicalNetwork: physnet.trim(), vlan, namespaces: selectedNs }
					: shared
						? { name, scope: 'shared', namespaces: selectedNs }
						: { name, namespace, scope: 'project' };
			if (subnet.trim()) req.subnets = [subnet.trim()];
			await api.createNetwork(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title={`New ${TERMS.segment.nsx} · ${TERMS.segment.vsphere}`} {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<!-- Segment type: an overlay (Geneve) Layer 2 network, or a VLAN bridged to a
			     Tier-0 uplink. -->
		<div class="flex gap-2">
			<button
				onclick={() => (kind = 'overlay')}
				class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'overlay'
					? 'border-blue-500 bg-blue-50 text-blue-700'
					: 'border-slate-300 text-ink-soft'}"
			>
				<div class="font-medium">Overlay Segment</div>
				<div class="text-ink-faint">Internal · Geneve (Layer 2)</div>
			</button>
			{#if canManage}
				<button
					onclick={() => (kind = 'vlan')}
					class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'vlan'
						? 'border-blue-500 bg-blue-50 text-blue-700'
						: 'border-slate-300 text-ink-soft'}"
				>
					<div class="font-medium">VLAN Segment</div>
					<div class="text-ink-faint">Bridged to a Tier-0 uplink</div>
				</button>
			{/if}
		</div>

		<label class="block">
			<span class="text-ink-soft">Name</span>
			<input
				bind:value={name}
				placeholder="db-net"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>

		{#if kind === 'overlay'}
			<!-- An overlay segment is a single-project UDN, or a Layer2 CUDN shared across
				     several projects. -->
			{#if canManage}
				<div class="flex gap-2">
					<button
						onclick={() => (shared = false)}
						class="flex-1 rounded border px-3 py-2 text-left text-xs {!shared
							? 'border-blue-500 bg-blue-50 text-blue-700'
							: 'border-slate-300 text-ink-soft'}"
					>
						<div class="font-medium">This project</div>
						<div class="text-ink-faint">UDN · one namespace (Tier-1)</div>
					</button>
					<button
						onclick={() => (shared = true)}
						class="flex-1 rounded border px-3 py-2 text-left text-xs {shared
							? 'border-blue-500 bg-blue-50 text-blue-700'
							: 'border-slate-300 text-ink-soft'}"
					>
						<div class="font-medium">Shared</div>
						<div class="text-ink-faint">CUDN · selected projects</div>
					</button>
				</div>
			{/if}
			{#if !shared}
				<label class="block">
					<span class="text-ink-soft">Project (namespace)</span>
					<select
						bind:value={namespace}
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					>
						{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
					</select>
				</label>
			{/if}
		{:else}
			<div class="grid grid-cols-2 gap-3">
				<label class="block">
					<span class="text-ink-soft">VLAN ID</span>
					<input
						type="number"
						bind:value={vlan}
						placeholder="200"
						min="1"
						max="4094"
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					/>
				</label>
				<label class="block">
					<span class="flex items-center justify-between text-ink-soft"
						>Uplink ({TERMS.uplink.nsx}){#if onAddUplink}<button
								type="button"
								onclick={onAddUplink}
								class="text-xs font-normal text-blue-600 hover:underline">+ Add uplink…</button
							>{/if}</span
					>
					<input
						bind:value={physnet}
						placeholder="physnet-prod"
						list="uplink-list"
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					/>
					<datalist id="uplink-list">
						{#each uplinks as u (u.name)}<option value={u.name}></option>{/each}
					</datalist>
				</label>
			</div>
		{/if}

		{#if isShared}
			<div>
				<span class="text-ink-soft">Published to projects</span>
				<div class="mt-1 max-h-28 space-y-1 overflow-y-auto rounded border border-slate-300 p-2">
					{#each namespaces as ns (ns)}
						<label class="flex items-center gap-2 text-xs">
							<input
								type="checkbox"
								checked={selectedNs.includes(ns)}
								onchange={(e) => toggleNs(ns, e.currentTarget.checked)}
							/>
							<span class="text-ink-soft">{ns}</span>
						</label>
					{/each}
				</div>
			</div>
		{/if}

		<label class="block">
			<span class="text-ink-soft"
				>Subnet <span class="text-ink-faint">(optional CIDR; blank = no IPAM)</span></span
			>
			<input
				bind:value={subnet}
				placeholder="10.20.0.0/24"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>

		<p class="rounded bg-slate-50 px-3 py-2 text-xs text-ink-muted">
			{#if kind === 'overlay'}
				An isolated overlay segment (Layer 2){shared
					? ', shared across the selected projects — a cluster-scoped CUDN, proposed to the platform repository'
					: ' scoped to this project (a namespace UDN on its Tier-1)'}.
			{:else}
				A VLAN segment (localnet) bridged to the chosen {TERMS.uplink.nsx.toLowerCase()}, published
				to the selected projects. Cluster-scoped — proposed to the platform repository; the uplink
				must already carry that physical network.
			{/if}
		</p>
		<p class="px-1 text-[11px] text-ink-faint">
			Looking for a project's default network? That is the primary {dual(TERMS.tier1)} segment — create
			it with a New Namespace or New Project.
		</p>
		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage segment"
			disabled={!valid}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
