<script lang="ts">
	import { api, type NetworkCreate, type Uplink } from '$lib/api';
	import { validName, NAME_HINT, validCIDR, CIDR_HINT } from '$lib/validate';
	import { TERMS, dual } from '$lib/vocab';
	import ChoiceCards from './ChoiceCards.svelte';
	import CheckGroup from './CheckGroup.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import NamespaceSelect from './NamespaceSelect.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';

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
	let share = $state<'project' | 'shared'>('project'); // overlay: one namespace (UDN) vs selected projects (CUDN)
	let vlan = $state<number | undefined>(undefined);
	let physnet = $state('');
	let selectedNs = $state<string[]>([]);

	let submitting = $state(false);
	let error = $state('');

	const kindOptions = $derived([
		{ value: 'overlay' as const, label: 'Overlay Segment', hint: 'Internal · Geneve (Layer 2)' },
		...(canManage
			? [{ value: 'vlan' as const, label: 'VLAN Segment', hint: 'Bridged to a Tier-0 uplink' }]
			: []),
	]);

	// A cluster-scoped (platform-tier) segment — a shared overlay or any VLAN — carries
	// a namespace multiselect: the set of projects it is published to. Mirrors the
	// backend routing cluster-scoped creates to the platform repo.
	const isShared = $derived(kind === 'vlan' || share === 'shared');

	// Same constraint the API server enforces; the server-side netgen validation
	// stays authoritative.
	const nameOK = $derived(validName(name));
	const vlanOK = $derived(vlan !== undefined && vlan >= 1 && vlan <= 4094);
	const subnetOK = $derived(!subnet.trim() || validCIDR(subnet.trim()));

	// Unmet requirements, in field order; drives both the disabled state and the
	// footer's explanation of it.
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!nameOK) m.push('Name must be lowercase alphanumeric and "-" (max 63 chars)');
		if (kind === 'vlan') {
			if (!vlanOK) m.push('VLAN ID (1-4094) is required');
			if (!physnet.trim()) m.push('Uplink is required');
		}
		if (isShared && !selectedNs.length) m.push('Select at least one project');
		if (kind === 'overlay' && !isShared && !namespace) m.push('Project is required');
		if (!subnetOK) m.push('Subnet must be a CIDR (e.g. 10.20.0.0/24)');
		return m;
	});
	const valid = $derived(missing.length === 0);

	// What this submission stages, in the footer — the counterpart of the
	// wizards' review step for a single-pane dialog.
	const summary = $derived.by(() => {
		if (!valid) return '';
		if (kind === 'vlan')
			return `Stages VLAN ${vlan} segment “${name}” on ${physnet.trim()} → platform repo, published to ${selectedNs.length} project${selectedNs.length === 1 ? '' : 's'}`;
		if (share === 'shared')
			return `Stages shared segment “${name}” (CUDN) → platform repo, published to ${selectedNs.length} project${selectedNs.length === 1 ? '' : 's'}`;
		return `Stages segment “${name}” (UDN) → ${namespace}`;
	});

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		try {
			const req: NetworkCreate =
				kind === 'vlan'
					? { name, scope: 'vlan', physicalNetwork: physnet.trim(), vlan, namespaces: selectedNs }
					: share === 'shared'
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
		<ChoiceCards options={kindOptions} bind:value={kind} />

		<FormField label="Name" error={name && !nameOK ? NAME_HINT : ''}>
			<TextInput bind:value={name} placeholder="db-net" mono data-autofocus />
		</FormField>

		{#if kind === 'overlay'}
			<!-- An overlay segment is a single-project UDN, or a Layer2 CUDN shared across
				     several projects. -->
			{#if canManage}
				<ChoiceCards
					options={[
						{ value: 'project', label: 'This project', hint: 'UDN · one namespace (Tier-1)' },
						{ value: 'shared', label: 'Shared', hint: 'CUDN · selected projects' },
					]}
					bind:value={share}
				/>
			{/if}
			{#if share !== 'shared'}
				<NamespaceSelect bind:namespace {namespaces} />
			{/if}
		{:else}
			<div class="grid grid-cols-2 gap-3">
				<FormField
					label="VLAN ID"
					error={vlan !== undefined && !vlanOK ? 'Between 1 and 4094.' : ''}
				>
					<TextInput type="number" bind:value={vlan} placeholder="200" min="1" max="4094" />
				</FormField>
				<label class="block">
					<span class="mb-1 flex items-center justify-between text-ink-soft"
						>Uplink ({TERMS.uplink.nsx}){#if onAddUplink}<button
								type="button"
								onclick={onAddUplink}
								class="text-xs font-normal text-accent hover:underline">+ Add uplink…</button
							>{/if}</span
					>
					<TextInput bind:value={physnet} placeholder="physnet-prod" mono list="uplink-list" />
					<datalist id="uplink-list">
						{#each uplinks as u (u.name)}<option value={u.name}></option>{/each}
					</datalist>
				</label>
			</div>
		{/if}

		{#if isShared}
			<div>
				<span class="mb-1 block text-ink-soft">Published to projects</span>
				<CheckGroup items={namespaces.map((ns) => ({ value: ns }))} bind:selected={selectedNs} />
			</div>
		{/if}

		<FormField
			label="Subnet (optional CIDR; blank = no IPAM)"
			error={subnet && !subnetOK ? CIDR_HINT : ''}
		>
			<TextInput bind:value={subnet} placeholder="10.20.0.0/24" mono />
		</FormField>

		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			{#if kind === 'overlay'}
				An isolated overlay segment (Layer 2){share === 'shared'
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
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage segment"
			disabled={!valid}
			{missing}
			{summary}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
