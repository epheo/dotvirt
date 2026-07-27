<script lang="ts">
	import { Plus, Trash2 } from 'lucide-svelte';
	import { api, type NetworkPolicyCreate, type PolicyRule, type VM } from '$lib/api';
	import { rowList } from '$lib/rowlist.svelte';
	import { TERMS } from '$lib/vocab';
	import { validName, NAME_HINT } from '$lib/validate';
	import Note from './Note.svelte';
	import StageModal from './StageModal.svelte';
	import NamespaceSelect from './NamespaceSelect.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import PeerSelector from './PeerSelector.svelte';
	import ProtoPortInput from './ProtoPortInput.svelte';

	let {
		namespaces,
		namespace: initial,
		vms = [],
		onclose,
		onstaged,
	}: {
		namespaces: string[];
		namespace?: string; // preselected namespace
		vms?: VM[]; // for the live "effective members" preview
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// A Group is a label selector (key=value) — the same primitive NSX-T's dynamic
	// Groups compile to. The policy protects the "applied-to" Group and allows ingress
	// only from the peer Groups in its rules (a NetworkPolicy that selects pods
	// default-denies all other ingress). One ingress row = one allow-from rule.
	type Row = { key: string; value: string; proto: 'TCP' | 'UDP' | 'SCTP'; port: number | null };
	const blankRow = (): Row => ({ key: '', value: '', proto: 'TCP', port: null });

	let name = $state('');
	let namespace = $state('');
	let appliedKey = $state(''); // applied-to Group; empty = the whole namespace
	let appliedValue = $state('');
	const rules = rowList(blankRow);
	const rows = $derived(rules.rows);

	// Effective members: VMs in the namespace whose labels match the applied-to Group
	// (every VM in the namespace when no selector is set) — the NSX-T "effective
	// membership" readout, computed live from the inventory.
	const members = $derived(
		vms.filter(
			(v) => v.namespace === namespace && (!appliedKey || v.labels?.[appliedKey] === appliedValue),
		),
	);

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push('Name must be lowercase alphanumeric with dashes');
		if (!namespace) m.push('Project is required');
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages DFW policy “${name}” → ${namespace} (${members.length} member VM${members.length === 1 ? '' : 's'})`
			: '',
	);

	async function stage() {
		const ingress: PolicyRule[] = [];
		for (const r of rows) {
			const rule: PolicyRule = {};
			if (r.key.trim()) rule.from = [{ [r.key.trim()]: r.value.trim() }];
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			// Skip wholly-empty rows (they would allow all traffic, defeating the policy).
			if (rule.from || rule.ports) ingress.push(rule);
		}
		const req: NetworkPolicyCreate = { name, namespace };
		if (appliedKey.trim()) req.appliedTo = { [appliedKey.trim()]: appliedValue.trim() };
		if (ingress.length) req.ingress = ingress;
		await api.createNetworkPolicy(req);
	}
</script>

<StageModal
	title={`${TERMS.dfw.nsx} · ${TERMS.dfw.vsphere}`}
	size="lg"
	label="Stage policy"
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<div class="grid grid-cols-2 gap-3">
		<FormField label="Name" error={name && !validName(name) ? NAME_HINT : ''}>
			<TextInput bind:value={name} placeholder="web-allow-db" mono data-autofocus />
		</FormField>
		<NamespaceSelect bind:namespace {namespaces} {initial} />
	</div>

	<div class="rounded border border-line p-3">
		<span class="text-ink-soft"
			>Applies to {TERMS.group.nsx}
			<span class="text-ink-faint">(label; blank = whole project)</span></span
		>
		<div class="mt-1 flex items-center gap-2">
			<PeerSelector
				bind:key={appliedKey}
				bind:value={appliedValue}
				keyPlaceholder="app"
				valuePlaceholder="db"
			/>
		</div>
		<div class="mt-1.5 text-[11px] text-ink-muted">
			Effective members: <span class="font-medium text-ink-soft">{members.length}</span>
			VM{members.length === 1 ? '' : 's'}
			{#if members.length}<span class="text-ink-faint"
					>— {members
						.slice(0, 6)
						.map((v) => v.name)
						.join(', ')}{members.length > 6 ? '…' : ''}</span
				>{/if}
		</div>
	</div>

	<div class="space-y-2">
		<div class="flex items-center justify-between">
			<span class="text-ink-soft">Allow ingress from</span>
			<button
				onclick={rules.add}
				class="flex items-center gap-1 text-xs text-accent hover:underline"
				><Plus size={12} /> Add source</button
			>
		</div>
		{#each rows as row, i (i)}
			<div class="flex flex-wrap items-center gap-2 rounded border border-line p-2">
				<span class="text-xs text-ink-faint">{TERMS.group.nsx}</span>
				<PeerSelector
					bind:key={row.key}
					bind:value={row.value}
					keyPlaceholder="app"
					valuePlaceholder="web"
					keyClass="w-20"
					valueClass="w-24"
				/>
				<ProtoPortInput bind:proto={row.proto} bind:port={row.port} portClass="w-20" />
				<button
					onclick={() => rules.remove(i)}
					disabled={rows.length === 1}
					aria-label="Remove source"
					class="ml-auto text-ink-faint hover:text-danger disabled:opacity-40"
					><Trash2 size={14} /></button
				>
			</div>
		{/each}
	</div>

	<Note tone="neutral">
		The {TERMS.dfw.nsx.toLowerCase()} controls east-west, VM-to-VM traffic. Selecting a {TERMS.group
			.nsx} default-denies all other ingress to it, so only the sources above may reach it. Staged into
		the project's repo and applied by its Argo app.
	</Note>
</StageModal>
