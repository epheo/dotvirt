<script lang="ts">
	import { Plus, Trash2 } from 'lucide-svelte';
	import { api, type AdminNetworkPolicyCreate, type AdminPolicyRule } from '$lib/api';
	import Modal from './Modal.svelte';

	let {
		onclose,
		onstaged,
	}: {
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// The cluster-wide admin DFW tier. An AdminNetworkPolicy is priority-ordered and
	// can Allow/Deny/Pass (Pass defers to tenant NetworkPolicies); the baseline is the
	// singleton default that backstops everything, Allow/Deny only. Subject and peers
	// are namespace selectors — Groups of projects. Cluster-scoped + admin-only, so it
	// is proposed to the platform repo and gated like a CUDN.
	// port is number | null, not string: <input type="number"> coerces its binding to
	// a number (or null when cleared), so a string type would make `.trim()` throw.
	type Row = {
		action: 'Allow' | 'Deny' | 'Pass';
		key: string;
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
	};
	const blankRow = (): Row => ({ action: 'Allow', key: '', value: '', proto: 'TCP', port: null });

	let baseline = $state(false);
	let name = $state('');
	let priority = $state<number | null>(10);
	let subjKey = $state('');
	let subjValue = $state('');
	let rows = $state<Row[]>([blankRow()]);
	let submitting = $state(false);
	let error = $state('');

	const valid = $derived(baseline || (!!name && priority != null));

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
		const ingress: AdminPolicyRule[] = [];
		for (const r of rows) {
			// Skip an untouched default row so it can't silently ship an "Allow from all
			// namespaces" rule; an explicit Deny/Pass or any configured peer/port is kept
			// (an empty {} peer is a legitimate "all namespaces" selector once intended).
			if (r.action === 'Allow' && !r.key.trim() && r.port == null) continue;
			const rule: AdminPolicyRule = {
				action: r.action,
				// An empty selector ({}) is a valid "all namespaces" peer.
				peers: [r.key.trim() ? { [r.key.trim()]: r.value.trim() } : {}],
			};
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			ingress.push(rule);
		}
		const req: AdminNetworkPolicyCreate = { name: baseline ? 'default' : name };
		if (baseline) req.baseline = true;
		else if (priority != null) req.priority = priority;
		if (subjKey.trim()) req.subject = { [subjKey.trim()]: subjValue.trim() };
		if (ingress.length) req.ingress = ingress;
		try {
			await api.createAdminNetworkPolicy(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="Admin Distributed Firewall" subtitle="cluster-wide" size="lg" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<div class="flex gap-2">
			<button
				onclick={() => (baseline = false)}
				class="flex-1 rounded border px-3 py-2 text-left text-xs {!baseline
					? 'border-blue-500 bg-blue-50 text-blue-700'
					: 'border-slate-300 text-ink-soft'}"
			>
				<div class="font-medium">Admin Policy</div>
				<div class="text-ink-faint">Priority-ordered · overrides tenants</div>
			</button>
			<button
				onclick={() => (baseline = true)}
				class="flex-1 rounded border px-3 py-2 text-left text-xs {baseline
					? 'border-blue-500 bg-blue-50 text-blue-700'
					: 'border-slate-300 text-ink-soft'}"
			>
				<div class="font-medium">Baseline</div>
				<div class="text-ink-faint">The cluster default backstop</div>
			</button>
		</div>

		<div class="grid grid-cols-2 gap-3">
			<label class="block">
				<span class="text-ink-soft">Name</span>
				<input
					bind:value={name}
					disabled={baseline}
					placeholder={baseline ? 'default' : 'tenant-isolation'}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5 disabled:bg-slate-100 disabled:text-ink-faint"
				/>
			</label>
			<label class="block">
				<span class="text-ink-soft">Priority <span class="text-ink-faint">(0–1000)</span></span>
				<input
					type="number"
					bind:value={priority}
					disabled={baseline}
					min="0"
					max="1000"
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5 disabled:bg-slate-100 disabled:text-ink-faint"
				/>
			</label>
		</div>

		<div class="rounded border border-slate-200 p-3">
			<span class="text-ink-soft"
				>Applies to project Group <span class="text-ink-faint">(namespace label; blank = all)</span
				></span
			>
			<div class="mt-1 flex items-center gap-2">
				<input
					bind:value={subjKey}
					placeholder="tier"
					class="min-w-0 flex-1 rounded border border-slate-300 px-2 py-1 text-xs"
				/>
				<span class="text-ink-faint">=</span>
				<input
					bind:value={subjValue}
					placeholder="prod"
					class="min-w-0 flex-1 rounded border border-slate-300 px-2 py-1 text-xs"
				/>
			</div>
		</div>

		<div class="space-y-2">
			<div class="flex items-center justify-between">
				<span class="text-ink-soft"
					>Ingress rules <span class="text-ink-faint">(ordered)</span></span
				>
				<button
					onclick={addRow}
					class="flex items-center gap-1 text-xs text-blue-600 hover:underline"
					><Plus size={12} /> Add rule</button
				>
			</div>
			{#each rows as row, i (i)}
				<div class="flex flex-wrap items-center gap-2 rounded border border-slate-200 p-2">
					<select
						bind:value={row.action}
						class="rounded border border-slate-300 px-2 py-1 text-xs {row.action === 'Deny'
							? 'text-red-700'
							: row.action === 'Allow'
								? 'text-green-700'
								: 'text-ink-soft'}"
					>
						<option value="Allow">Allow</option>
						<option value="Deny">Deny</option>
						{#if !baseline}<option value="Pass">Pass</option>{/if}
					</select>
					<span class="text-xs text-ink-faint">from project</span>
					<input
						bind:value={row.key}
						placeholder="tier"
						class="w-20 rounded border border-slate-300 px-2 py-1 text-xs"
					/>
					<span class="text-ink-faint">=</span>
					<input
						bind:value={row.value}
						placeholder="web"
						class="w-20 rounded border border-slate-300 px-2 py-1 text-xs"
					/>
					<span class="text-xs text-ink-faint">port</span>
					<select
						bind:value={row.proto}
						class="rounded border border-slate-300 px-1.5 py-1 text-xs"
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
						class="w-16 rounded border border-slate-300 px-2 py-1 text-xs"
					/>
					<button
						onclick={() => removeRow(i)}
						disabled={rows.length === 1}
						aria-label="Remove rule"
						class="ml-auto text-ink-faint hover:text-red-600 disabled:opacity-40"
						><Trash2 size={14} /></button
					>
				</div>
			{/each}
		</div>

		<p class="rounded bg-amber-50 px-3 py-2 text-xs text-amber-700">
			Cluster-wide and admin-only. {#if baseline}The baseline is the default backstop applied
				beneath every tenant NetworkPolicy.{:else}An Admin Policy overrides tenant NetworkPolicies —
				use <strong>Pass</strong> to defer a decision back to them.{/if} Proposed to the platform repository.
		</p>
		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<span class="text-xs text-ink-faint">Staged into the changeset; open a PR from “Changes”.</span>
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-slate-100">Cancel</button
		>
		<button
			onclick={submit}
			disabled={!valid || submitting}
			class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
		>
			{submitting ? 'Staging…' : 'Stage policy'}
		</button>
	{/snippet}
</Modal>
