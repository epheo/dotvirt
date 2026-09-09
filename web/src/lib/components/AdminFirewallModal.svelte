<script lang="ts">
	import { Plus, Trash2 } from 'lucide-svelte';
	import { api, type AdminNetworkPolicyCreate, type AdminPolicyRule } from '$lib/api';
	import { rowList } from '$lib/rowlist.svelte';
	import { validName, NAME_HINT } from '$lib/validate';
	import ChoiceCards from './ChoiceCards.svelte';
	import Note from './Note.svelte';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import PeerSelector from './PeerSelector.svelte';
	import ProtoPortInput from './ProtoPortInput.svelte';

	let {
		initial,
		onclose,
		onstaged,
	}: {
		initial?: AdminNetworkPolicyCreate; // the policy as git declares it: edit, not create
		onclose: () => void;
		onstaged: () => void;
	} = $props();
	// svelte-ignore state_referenced_locally
	const editing = !!initial;

	// The cluster-wide admin DFW tier. An AdminNetworkPolicy is priority-ordered and
	// can Allow/Deny/Pass (Pass defers to tenant NetworkPolicies); the baseline is the
	// singleton default that backstops everything, Allow/Deny only. Subject and peers
	// are namespace selectors - Groups of projects. Cluster-scoped + admin-only, so it
	// is proposed to the platform repo and gated like a CUDN.
	type Row = {
		action: 'Allow' | 'Deny' | 'Pass';
		key: string;
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
	};
	const blankRow = (): Row => ({ action: 'Allow', key: '', value: '', proto: 'TCP', port: null });

	// svelte-ignore state_referenced_locally
	let tier = $state<'policy' | 'baseline'>(initial?.baseline ? 'baseline' : 'policy');
	const baseline = $derived(tier === 'baseline');
	// svelte-ignore state_referenced_locally
	const [initialKey, initialValue] = Object.entries(initial?.subject ?? {})[0] ?? ['', ''];
	// svelte-ignore state_referenced_locally
	let name = $state(initial?.name ?? '');
	// svelte-ignore state_referenced_locally
	let priority = $state<number | null>(initial ? (initial.priority ?? 0) : 10);
	let subjKey = $state(initialKey);
	let subjValue = $state(initialValue);
	const rules = rowList(
		blankRow,
		// svelte-ignore state_referenced_locally
		initial?.ingress?.map((r) => {
			const [key, value] = Object.entries(r.peers[0] ?? {})[0] ?? ['', ''];
			const port = r.ports?.[0];
			return {
				action: r.action,
				key,
				value,
				proto: port?.protocol ?? 'TCP',
				port: port?.port ?? null,
			};
		}),
	);
	const rows = $derived(rules.rows);

	const missing = $derived.by(() => {
		if (baseline) return [];
		const m: string[] = [];
		if (!name) m.push('Name is required');
		else if (!validName(name)) m.push('Name must be lowercase alphanumeric with dashes');
		if (priority == null || priority < 0 || priority > 1000) m.push('Priority must be 0-1000');
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		!valid
			? ''
			: baseline
				? `${editing ? 'Updates' : 'Stages'} the baseline policy (cluster default backstop) → platform repo`
				: `${editing ? 'Updates' : 'Stages'} admin policy “${name}” (priority ${priority}) → platform repo`,
	);

	async function stage() {
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
		await api.createAdminNetworkPolicy(req);
	}
</script>

<StageModal
	title={editing ? `Edit admin policy · ${name}` : 'Admin Distributed Firewall · cluster-wide'}
	size="lg"
	label={editing ? 'Stage changes' : 'Stage policy'}
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	{#if !editing}
		<ChoiceCards
			options={[
				{ value: 'policy', label: 'Admin Policy', hint: 'Priority-ordered · overrides tenants' },
				{ value: 'baseline', label: 'Baseline', hint: 'The cluster default backstop' },
			]}
			bind:value={tier}
		/>
	{/if}

	<div class="grid grid-cols-2 gap-3">
		<FormField label="Name" error={!baseline && name && !validName(name) ? NAME_HINT : ''}>
			<TextInput
				bind:value={name}
				disabled={baseline || editing}
				placeholder={baseline ? 'default' : 'tenant-isolation'}
				mono
			/>
		</FormField>
		<FormField label="Priority (0–1000)">
			<TextInput type="number" bind:value={priority} disabled={baseline} min="0" max="1000" />
		</FormField>
	</div>

	<div class="rounded border border-line p-3">
		<span class="text-ink-soft"
			>Applies to project Group <span class="text-ink-faint">(namespace label; blank = all)</span
			></span
		>
		<div class="mt-1 flex items-center gap-2">
			<PeerSelector
				bind:key={subjKey}
				bind:value={subjValue}
				keyPlaceholder="tier"
				valuePlaceholder="prod"
			/>
		</div>
	</div>

	<div class="space-y-2">
		<div class="flex items-center justify-between">
			<span class="text-ink-soft">Ingress rules <span class="text-ink-faint">(ordered)</span></span>
			<button
				onclick={rules.add}
				class="flex items-center gap-1 text-xs text-accent hover:underline"
				><Plus size={12} /> Add rule</button
			>
		</div>
		{#each rows as row, i (i)}
			<div class="flex flex-wrap items-center gap-2 rounded border border-line p-2">
				<select
					bind:value={row.action}
					class="rounded border border-line-strong px-2 py-1 text-xs {row.action === 'Deny'
						? 'text-danger-ink'
						: row.action === 'Allow'
							? 'text-ok-ink'
							: 'text-ink-soft'}"
				>
					<option value="Allow">Allow</option>
					<option value="Deny">Deny</option>
					{#if !baseline}<option value="Pass">Pass</option>{/if}
				</select>
				<span class="text-xs text-ink-faint">from project</span>
				<PeerSelector
					bind:key={row.key}
					bind:value={row.value}
					keyPlaceholder="tier"
					valuePlaceholder="web"
					keyClass="w-20"
					valueClass="w-20"
				/>
				<ProtoPortInput bind:proto={row.proto} bind:port={row.port} portClass="w-16" />
				<button
					onclick={() => rules.remove(i)}
					disabled={rows.length === 1}
					aria-label="Remove rule"
					class="ml-auto text-ink-faint hover:text-danger disabled:opacity-40"
					><Trash2 size={14} /></button
				>
			</div>
		{/each}
	</div>

	<Note tone="warn">
		Cluster-wide and admin-only. {#if baseline}The baseline is the default backstop applied beneath
			every tenant NetworkPolicy.{:else}An Admin Policy overrides tenant NetworkPolicies — use <strong
				>Pass</strong
			> to defer a decision back to them.{/if} Proposed to the platform repository.
	</Note>
</StageModal>
