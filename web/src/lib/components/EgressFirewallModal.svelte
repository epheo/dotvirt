<script lang="ts">
	import { Plus, Trash2, X } from 'lucide-svelte';
	import { api, type EgressFirewallCreate, type EgressFirewallRule } from '$lib/api';
	import { TERMS } from '$lib/vocab';

	let {
		namespaces,
		namespace: initial,
		onclose,
		onstaged
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
	// port is number | null, not string: <input type="number"> coerces its binding to
	// a number (or null when cleared), so a string type would make `.trim()` throw.
	type Row = {
		action: 'Allow' | 'Deny';
		dest: 'cidr' | 'dns';
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
	};
	const blank = (): Row => ({ action: 'Allow', dest: 'cidr', value: '', proto: 'TCP', port: null });

	let namespace = $state('');
	let rows = $state<Row[]>([blank()]);
	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = initial ?? namespaces[0] ?? '';
	});

	const valid = $derived(!!namespace && rows.length > 0 && rows.every((r) => r.value.trim()));

	function addRow() {
		rows = [...rows, blank()];
	}
	function removeRow(i: number) {
		rows = rows.filter((_, j) => j !== i);
	}

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		const rules: EgressFirewallRule[] = rows.map((r) => {
			const rule: EgressFirewallRule = { action: r.action };
			if (r.dest === 'cidr') rule.cidr = r.value.trim();
			else rule.dnsName = r.value.trim();
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			return rule;
		});
		const req: EgressFirewallCreate = { namespace, rules };
		try {
			await api.createEgressFirewall(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[90vh] w-full max-w-lg flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-slate-800">
				{TERMS.gatewayFirewall.nsx}
				<span class="font-normal text-slate-400">· {TERMS.gatewayFirewall.vsphere}</span>
			</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"
				><X size={18} /></button
			>
		</header>
		<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
			<label class="block">
				<span class="text-slate-600">Project (namespace)</span>
				<select
					bind:value={namespace}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				>
					{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
				</select>
			</label>

			<div class="space-y-2">
				<div class="flex items-center justify-between">
					<span class="text-slate-600"
						>Egress rules <span class="text-slate-400">(first match wins)</span></span
					>
					<button
						onclick={addRow}
						class="flex items-center gap-1 text-xs text-blue-600 hover:underline"
						><Plus size={12} /> Add rule</button
					>
				</div>
				{#each rows as row, i (i)}
					<div class="rounded border border-slate-200 p-2">
						<div class="flex flex-wrap items-center gap-2">
							<select
								bind:value={row.action}
								class="rounded border border-slate-300 px-2 py-1 text-xs {row.action === 'Deny'
									? 'text-red-700'
									: 'text-green-700'}"
							>
								<option value="Allow">Allow</option>
								<option value="Deny">Deny</option>
							</select>
							<span class="text-xs text-slate-400">egress to</span>
							<select
								bind:value={row.dest}
								class="rounded border border-slate-300 px-2 py-1 text-xs"
							>
								<option value="cidr">CIDR</option>
								<option value="dns">DNS name</option>
							</select>
							<input
								bind:value={row.value}
								placeholder={row.dest === 'cidr' ? '0.0.0.0/0' : 'api.example.com'}
								class="min-w-0 flex-1 rounded border border-slate-300 px-2 py-1 text-xs"
							/>
							<button
								onclick={() => removeRow(i)}
								disabled={rows.length === 1}
								aria-label="Remove rule"
								class="text-slate-300 hover:text-red-600 disabled:opacity-40"
								><Trash2 size={14} /></button
							>
						</div>
						<div class="mt-2 flex items-center gap-2 pl-1 text-xs text-slate-500">
							<span>port</span>
							<select bind:value={row.proto} class="rounded border border-slate-300 px-1.5 py-1">
								<option value="TCP">TCP</option>
								<option value="UDP">UDP</option>
								<option value="SCTP">SCTP</option>
							</select>
							<input
								type="number"
								bind:value={row.port}
								placeholder="any"
								min="1"
								max="65535"
								class="w-24 rounded border border-slate-300 px-2 py-1"
							/>
						</div>
					</div>
				{/each}
			</div>

			<p class="rounded bg-slate-50 px-3 py-2 text-xs text-slate-500">
				The {TERMS.gatewayFirewall.nsx.toLowerCase()} controls north-south traffic leaving this project's
				VMs to external destinations (it is not an east-west, VM-to-VM control — that is the Distributed
				Firewall). One per namespace; staged into the project's repo and applied by its Argo app.
			</p>
			{#if error}
				<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
			{/if}
		</div>
		<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
			<span class="text-xs text-slate-400"
				>Staged into the changeset; open a PR from “Changes”.</span
			>
			<button
				onclick={onclose}
				class="ml-auto rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Cancel</button
			>
			<button
				onclick={submit}
				disabled={!valid || submitting}
				class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
			>
				{submitting ? 'Staging…' : 'Stage firewall'}
			</button>
		</footer>
	</div>
</div>
