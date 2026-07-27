<script lang="ts">
	import { Plus, Trash2 } from 'lucide-svelte';
	import { api, type EgressFirewallCreate, type EgressFirewallRule } from '$lib/api';
	import { rowList } from '$lib/rowlist.svelte';
	import { TERMS } from '$lib/vocab';
	import Note from './Note.svelte';
	import StageModal from './StageModal.svelte';
	import NamespaceSelect from './NamespaceSelect.svelte';
	import ProtoPortInput from './ProtoPortInput.svelte';
	import SelectInput from './SelectInput.svelte';
	import TextInput from './TextInput.svelte';

	let {
		namespaces,
		namespace: initial,
		onclose,
		onstaged,
	}: {
		namespaces: string[];
		namespace?: string; // preselected namespace (e.g. from a namespace context menu)
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// One editable rule row. A rule allows or denies egress to a destination — a CIDR
	// or a DNS name (exactly one) — optionally narrowed to a single transport port.
	// (OVN-K rules carry a port list; one port per row covers the common case, and the
	// user can add more rows.)
	type Row = {
		action: 'Allow' | 'Deny';
		dest: 'cidr' | 'dns';
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
	};
	const blank = (): Row => ({ action: 'Allow', dest: 'cidr', value: '', proto: 'TCP', port: null });

	let namespace = $state('');
	const rules = rowList(blank);
	const rows = $derived(rules.rows);

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!namespace) m.push('Project is required');
		if (!rows.length) m.push('Add at least one rule');
		else if (rows.some((r) => !r.value.trim())) m.push('Every rule needs a destination');
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages egress firewall (${rows.length} rule${rows.length === 1 ? '' : 's'}) → ${namespace}`
			: '',
	);

	async function stage() {
		const ruleSpecs: EgressFirewallRule[] = rows.map((r) => {
			const rule: EgressFirewallRule = { action: r.action };
			if (r.dest === 'cidr') rule.cidr = r.value.trim();
			else rule.dnsName = r.value.trim();
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			return rule;
		});
		const req: EgressFirewallCreate = { namespace, rules: ruleSpecs };
		await api.createEgressFirewall(req);
	}
</script>

<StageModal
	title={`${TERMS.gatewayFirewall.nsx} · ${TERMS.gatewayFirewall.vsphere}`}
	size="lg"
	label="Stage firewall"
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<NamespaceSelect bind:namespace {namespaces} {initial} />

	<div class="space-y-2">
		<div class="flex items-center justify-between">
			<span class="text-ink-soft"
				>Egress rules <span class="text-ink-faint">(first match wins)</span></span
			>
			<button
				onclick={rules.add}
				class="flex items-center gap-1 text-xs text-accent hover:underline"
				><Plus size={12} /> Add rule</button
			>
		</div>
		{#each rows as row, i (i)}
			<div class="rounded border border-line p-2">
				<div class="flex flex-wrap items-center gap-2">
					<SelectInput
						bind:value={row.action}
						size="sm"
						class="w-auto! {row.action === 'Deny' ? 'text-danger-ink' : 'text-ok-ink'}"
					>
						<option value="Allow">Allow</option>
						<option value="Deny">Deny</option>
					</SelectInput>
					<span class="text-xs text-ink-faint">egress to</span>
					<SelectInput bind:value={row.dest} size="sm" class="w-auto!">
						<option value="cidr">CIDR</option>
						<option value="dns">DNS name</option>
					</SelectInput>
					<TextInput
						bind:value={row.value}
						size="sm"
						placeholder={row.dest === 'cidr' ? '0.0.0.0/0' : 'api.example.com'}
						class="min-w-0 flex-1"
					/>
					<button
						onclick={() => rules.remove(i)}
						disabled={rows.length === 1}
						aria-label="Remove rule"
						class="text-ink-faint hover:text-danger disabled:opacity-40"
						><Trash2 size={14} /></button
					>
				</div>
				<div class="mt-2 flex items-center gap-2 pl-1 text-xs text-ink-muted">
					<ProtoPortInput
						bind:proto={row.proto}
						bind:port={row.port}
						portClass="w-24"
						labelClass=""
					/>
				</div>
			</div>
		{/each}
	</div>

	<Note tone="neutral">
		The {TERMS.gatewayFirewall.nsx.toLowerCase()} controls north-south traffic leaving this project's
		VMs to external destinations (it is not an east-west, VM-to-VM control — that is the Distributed Firewall).
		One per namespace; staged into the project's repo and applied by its Argo app.
	</Note>
</StageModal>
