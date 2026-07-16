<script lang="ts">
	import { Plus, Trash2 } from 'lucide-svelte';
	import { api, type NetworkPolicyCreate, type PolicyRule, type VM } from '$lib/api';
	import { TERMS } from '$lib/vocab';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';

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
	// port is number | null, not string: <input type="number"> coerces its binding to
	// a number (or null when cleared), so a string type would make `.trim()` throw.
	type Row = { key: string; value: string; proto: 'TCP' | 'UDP' | 'SCTP'; port: number | null };
	const blankRow = (): Row => ({ key: '', value: '', proto: 'TCP', port: null });

	let name = $state('');
	let namespace = $state('');
	let appliedKey = $state(''); // applied-to Group; empty = the whole namespace
	let appliedValue = $state('');
	let rows = $state<Row[]>([blankRow()]);
	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!namespace) namespace = initial ?? namespaces[0] ?? '';
	});

	// Effective members: VMs in the namespace whose labels match the applied-to Group
	// (every VM in the namespace when no selector is set) — the NSX-T "effective
	// membership" readout, computed live from the inventory.
	const members = $derived(
		vms.filter(
			(v) => v.namespace === namespace && (!appliedKey || v.labels?.[appliedKey] === appliedValue),
		),
	);

	const valid = $derived(!!name && !!namespace);

	function addRow() {
		rows = [...rows, blankRow()];
	}
	function removeRow(i: number) {
		rows = rows.filter((_, j) => j !== i);
	}

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
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
		try {
			await api.createNetworkPolicy(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title={TERMS.dfw.nsx} subtitle={TERMS.dfw.vsphere} size="lg" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<div class="grid grid-cols-2 gap-3">
			<label class="block">
				<span class="text-ink-soft">Name</span>
				<input
					bind:value={name}
					placeholder="web-allow-db"
					class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
				/>
			</label>
			<label class="block">
				<span class="text-ink-soft">Project (namespace)</span>
				<select
					bind:value={namespace}
					class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
				>
					{#each namespaces as ns (ns)}<option value={ns}>{ns}</option>{/each}
				</select>
			</label>
		</div>

		<div class="rounded border border-line p-3">
			<span class="text-ink-soft"
				>Applies to {TERMS.group.nsx}
				<span class="text-ink-faint">(label; blank = whole project)</span></span
			>
			<div class="mt-1 flex items-center gap-2">
				<input
					bind:value={appliedKey}
					placeholder="app"
					class="min-w-0 flex-1 rounded border border-line-strong px-2 py-1 text-xs"
				/>
				<span class="text-ink-faint">=</span>
				<input
					bind:value={appliedValue}
					placeholder="db"
					class="min-w-0 flex-1 rounded border border-line-strong px-2 py-1 text-xs"
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
				<button onclick={addRow} class="flex items-center gap-1 text-xs text-accent hover:underline"
					><Plus size={12} /> Add source</button
				>
			</div>
			{#each rows as row, i (i)}
				<div class="flex flex-wrap items-center gap-2 rounded border border-line p-2">
					<span class="text-xs text-ink-faint">{TERMS.group.nsx}</span>
					<input
						bind:value={row.key}
						placeholder="app"
						class="w-20 rounded border border-line-strong px-2 py-1 text-xs"
					/>
					<span class="text-ink-faint">=</span>
					<input
						bind:value={row.value}
						placeholder="web"
						class="w-24 rounded border border-line-strong px-2 py-1 text-xs"
					/>
					<span class="text-xs text-ink-faint">port</span>
					<select
						bind:value={row.proto}
						class="rounded border border-line-strong px-1.5 py-1 text-xs"
					>
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
						class="w-20 rounded border border-line-strong px-2 py-1 text-xs"
					/>
					<button
						onclick={() => removeRow(i)}
						disabled={rows.length === 1}
						aria-label="Remove source"
						class="ml-auto text-ink-faint hover:text-danger disabled:opacity-40"
						><Trash2 size={14} /></button
					>
				</div>
			{/each}
		</div>

		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			The {TERMS.dfw.nsx.toLowerCase()} controls east-west, VM-to-VM traffic. Selecting a {TERMS
				.group.nsx} default-denies all other ingress to it, so only the sources above may reach it. Staged
			into the project's repo and applied by its Argo app.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<span class="text-xs text-ink-faint">Staged into the changeset; open a PR from “Changes”.</span>
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong">Cancel</button
		>
		<button
			onclick={submit}
			disabled={!valid || submitting}
			class="rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
		>
			{submitting ? 'Staging…' : 'Stage policy'}
		</button>
	{/snippet}
</Modal>
