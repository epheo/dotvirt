<script lang="ts">
	import { api, type EgressIPCreate, type ExternalRouteCreate } from '$lib/api';
	import { validName, NAME_HINT, validIP } from '$lib/validate';
	import { TERMS } from '$lib/vocab';
	import CheckGroup from './CheckGroup.svelte';
	import ChoiceCards from './ChoiceCards.svelte';
	import Note from './Note.svelte';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';

	let {
		namespaces,
		onclose,
		onstaged,
	}: {
		namespaces: string[];
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// A Tier-0 (provider-edge) service: a source-NAT pool (EgressIP) pinning a
	// project's egress to fixed, routable IPs, or an external route steering its egress
	// through static next-hop gateways (AdminPolicyBasedExternalRoute). Both are
	// cluster-scoped - proposed to the platform repo.
	let kind = $state<'snat' | 'route'>('snat');
	let name = $state('');
	let ips = $state(''); // egress IPs (snat) or next-hop IPs (route), space/comma separated
	let selectedNs = $state<string[]>([]);

	const list = $derived(
		ips
			.split(/[\s,]+/)
			.map((s) => s.trim())
			.filter(Boolean),
	);
	const badIPs = $derived(list.filter((ip) => !validIP(ip)));
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push('Name must be lowercase alphanumeric with dashes');
		if (!list.length)
			m.push(
				kind === 'snat'
					? 'At least one egress IP is required'
					: 'At least one next-hop IP is required',
			);
		else if (badIPs.length) m.push(`Not an IP: ${badIPs[0]}`);
		if (!selectedNs.length) m.push('Select at least one project');
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages ${kind === 'snat' ? TERMS.snat.nsx : 'external route'} “${name}” (${list.length} IP${list.length === 1 ? '' : 's'}, ${selectedNs.length} project${selectedNs.length === 1 ? '' : 's'}) → platform repo`
			: '',
	);

	async function stage() {
		if (kind === 'snat') {
			const req: EgressIPCreate = { name, egressIPs: list, namespaces: selectedNs };
			await api.createEgressIP(req);
		} else {
			const req: ExternalRouteCreate = { name, namespaces: selectedNs, nextHops: list };
			await api.createExternalRoute(req);
		}
	}
</script>

<StageModal
	title={`${TERMS.tier0.nsx} · ${TERMS.tier0.vsphere}`}
	label="Stage service"
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<ChoiceCards
		options={[
			{ value: 'snat', label: TERMS.snat.nsx, hint: 'Pin egress to fixed IPs (EgressIP)' },
			{ value: 'route', label: 'External Route', hint: 'Steer egress via next-hops' },
		]}
		bind:value={kind}
	/>

	<FormField label="Name" error={name && !validName(name) ? NAME_HINT : ''}>
		<TextInput
			bind:value={name}
			placeholder={kind === 'snat' ? 'team-a-snat' : 'team-a-gw'}
			mono
			data-autofocus
		/>
	</FormField>

	<FormField
		label={`${kind === 'snat' ? 'Egress IPs' : 'Next-hop IPs'} (space/comma separated)`}
		error={badIPs.length ? `Not an IP: ${badIPs[0]}` : ''}
	>
		<TextInput
			bind:value={ips}
			placeholder={kind === 'snat' ? '192.0.2.10 192.0.2.11' : '10.0.0.1'}
			mono
		/>
	</FormField>

	<div>
		<span class="mb-1 block text-ink-soft">Applies to projects</span>
		<CheckGroup items={namespaces.map((ns) => ({ value: ns }))} bind:selected={selectedNs} />
	</div>

	<Note tone="neutral">
		{#if kind === 'snat'}
			A {TERMS.snat.nsx} pool ({TERMS.snat.backing}) pins the selected projects' north-south egress
			to these fixed, routable source IPs.
		{:else}
			An external route (AdminPolicyBasedExternalRoute) steers the selected projects' egress through
			these static next-hop gateways.
		{/if}
		Cluster-scoped — proposed to the platform repository.
	</Note>
</StageModal>
