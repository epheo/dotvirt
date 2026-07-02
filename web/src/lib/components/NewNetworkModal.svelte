<script lang="ts">
	import { api, type NetworkCreate, type Uplink } from '$lib/api';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	let {
		namespaces,
		projects = [],
		uplinks = [],
		canManage = false,
		onclose,
		onstaged,
		onAddUplink
	}: {
		namespaces: string[];
		projects?: string[]; // repo-backed projects (the joining project for a VM Network)
		uplinks?: Uplink[]; // discovered uplinks (physnet hints for VLAN)
		canManage?: boolean; // caller may author platform-tier networks (CUDN / VLAN / VM Network)
		onclose: () => void;
		onstaged: () => void;
		onAddUplink?: () => void; // open the Add Uplink wizard (offered from the VLAN flow)
	} = $props();

	let kind = $state<'isolated' | 'vlan' | 'vmnetwork'>('isolated');
	let name = $state('');
	let subnet = $state('');
	// Isolated: a single-namespace UDN, or — when shared — a Layer2 CUDN across namespaces.
	let namespace = $state('');
	let isoShared = $state(false); // false = this namespace (UDN), true = shared across namespaces (CUDN)
	// VLAN (localnet CUDN):
	let vlan = $state<number | undefined>(undefined);
	let physnet = $state('');
	let targetProject = $state('');
	let selectedNs = $state<string[]>([]);
	// VM Network (primary Layer2 UDN — creates a new namespace):
	let nsName = $state('');

	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = namespaces[0] ?? '';
	});
	$effect(() => {
		if (!targetProject) targetProject = projects[0] ?? '';
	});

	// A target repo + namespace multiselect are shown for any cluster-scoped CUDN: a
	// VLAN network, or an isolated network shared across namespaces.
	const needsSharedTargeting = $derived(kind === 'vlan' || (kind === 'isolated' && isoShared));

	const valid = $derived(
		kind === 'isolated'
			? isoShared
				? !!(name && selectedNs.length)
				: !!(name && namespace)
			: kind === 'vlan'
				? !!(name && physnet.trim() && vlan && selectedNs.length)
				: !!(name && nsName && targetProject && subnet.trim())
	);

	function toggleNs(ns: string, on: boolean) {
		selectedNs = on ? [...selectedNs, ns] : selectedNs.filter((n) => n !== ns);
	}

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		try {
			if (kind === 'vmnetwork') {
				// A VM Network is a primary UDN, which needs a fresh namespace — so this
				// creates the namespace + its default network together.
				await api.createNamespace({
					name: nsName,
					project: targetProject,
					vmNetwork: { name, subnet: subnet.trim() }
				});
			} else {
				const req: NetworkCreate =
					kind === 'isolated'
						? isoShared
							? { name, scope: 'shared', namespaces: selectedNs }
							: { name, namespace, scope: 'project' }
						: {
								name,
								scope: 'vlan',
								physicalNetwork: physnet.trim(),
								vlan,
								namespaces: selectedNs
							};
				if (subnet.trim()) req.subnets = [subnet.trim()];
				await api.createNetwork(req);
			}
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="New Distributed Port Group" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<!-- Connectivity: an isolated internal segment, or a VLAN bridged to an uplink. -->
		<div class="flex gap-2">
			<button
				onclick={() => (kind = 'isolated')}
				class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'isolated'
					? 'border-blue-500 bg-blue-50 text-blue-700'
					: 'border-slate-300 text-slate-600'}"
			>
				<div class="font-medium">Isolated</div>
				<div class="text-slate-400">Internal, no uplink (Layer 2)</div>
			</button>
			{#if canManage}
				<button
					onclick={() => (kind = 'vlan')}
					class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'vlan'
						? 'border-blue-500 bg-blue-50 text-blue-700'
						: 'border-slate-300 text-slate-600'}"
				>
					<div class="font-medium">VLAN</div>
					<div class="text-slate-400">Bridged to an uplink, datacenter-wide</div>
				</button>
				<button
					onclick={() => (kind = 'vmnetwork')}
					class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'vmnetwork'
						? 'border-blue-500 bg-blue-50 text-blue-700'
						: 'border-slate-300 text-slate-600'}"
				>
					<div class="font-medium">VM Network</div>
					<div class="text-slate-400">A new namespace's default network</div>
				</button>
			{/if}
		</div>

		<label class="block">
			<span class="text-slate-600">Name</span>
			<input
				bind:value={name}
				placeholder="db-net"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>

		{#if kind === 'isolated'}
			<!-- An isolated network is a single-namespace UDN, or a Layer2 CUDN shared
				     across several namespaces. -->
			{#if canManage}
				<div class="flex gap-2">
					<button
						onclick={() => (isoShared = false)}
						class="flex-1 rounded border px-3 py-2 text-left text-xs {!isoShared
							? 'border-blue-500 bg-blue-50 text-blue-700'
							: 'border-slate-300 text-slate-600'}"
					>
						<div class="font-medium">This namespace</div>
						<div class="text-slate-400">One project (UDN)</div>
					</button>
					<button
						onclick={() => (isoShared = true)}
						class="flex-1 rounded border px-3 py-2 text-left text-xs {isoShared
							? 'border-blue-500 bg-blue-50 text-blue-700'
							: 'border-slate-300 text-slate-600'}"
					>
						<div class="font-medium">Multiple namespaces</div>
						<div class="text-slate-400">Shared across projects (CUDN)</div>
					</button>
				</div>
			{/if}
			{#if !isoShared}
				<label class="block">
					<span class="text-slate-600">Project (namespace)</span>
					<select
						bind:value={namespace}
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					>
						{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
					</select>
				</label>
			{/if}
		{:else if kind === 'vmnetwork'}
			<label class="block">
				<span class="text-slate-600">New namespace</span>
				<input
					bind:value={nsName}
					placeholder="tenant-c"
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>
			<label class="block">
				<span class="text-slate-600">Project</span>
				<select
					bind:value={targetProject}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				>
					{#each projects as p (p)}<option value={p}>{p}</option>{/each}
				</select>
			</label>
		{:else}
			<div class="grid grid-cols-2 gap-3">
				<label class="block">
					<span class="text-slate-600">VLAN ID</span>
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
					<span class="flex items-center justify-between text-slate-600"
						>Uplink (physical network){#if onAddUplink}<button
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

		{#if needsSharedTargeting}
			<div>
				<span class="text-slate-600"
					>{kind === 'vlan' ? 'Available in namespaces' : 'Namespaces to share with'}</span
				>
				<div class="mt-1 max-h-28 space-y-1 overflow-y-auto rounded border border-slate-300 p-2">
					{#each namespaces as ns (ns)}
						<label class="flex items-center gap-2 text-xs">
							<input
								type="checkbox"
								checked={selectedNs.includes(ns)}
								onchange={(e) => toggleNs(ns, e.currentTarget.checked)}
							/>
							<span class="text-slate-700">{ns}</span>
						</label>
					{/each}
				</div>
			</div>
		{/if}

		<label class="block">
			<span class="text-slate-600"
				>Subnet <span class="text-slate-400"
					>{kind === 'vmnetwork'
						? '(CIDR — required for a primary network)'
						: '(optional CIDR; blank = no IPAM)'}</span
				></span
			>
			<input
				bind:value={subnet}
				placeholder="10.20.0.0/24"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>

		<p class="rounded bg-slate-50 px-3 py-2 text-xs text-slate-500">
			{#if kind === 'isolated'}
				An internal, isolated port group (Layer 2){isoShared
					? ', shared across the selected namespaces — cluster-scoped, proposed to the platform repository'
					: ' scoped to this project'}.
			{:else if kind === 'vlan'}
				A VLAN-backed port group (localnet) bridged to the chosen uplink, published to the selected
				namespaces. Cluster-scoped — proposed to the platform repository; the uplink must already
				carry that physical network.
			{:else}
				A VM Network is a namespace's default (primary) network, so it creates a new namespace in
				the project with this network attached. Applied by the project's Argo app on merge.
			{/if}
		</p>
		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage network"
			disabled={!valid}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
