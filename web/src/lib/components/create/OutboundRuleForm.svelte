<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { Plus, X } from '@lucide/svelte';
	import { api, type EgressFirewallCreate, type EgressFirewallRule } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { rowList } from '#lib/rowlist.svelte.js';
	import { objectGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '../Button.svelte';
	import Consequence from '../Consequence.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, SMALL, stagePath, useSheet } from './sheet.svelte.js';

	// Outbound rules: where a namespace's VMs may go outside the cluster, first
	// match wins. A namespace has one set; staging replaces it.
	let {
		namespace = $bindable(),
		initial,
		chooser,
		onclose,
	}: {
		namespace: string;
		initial?: EgressFirewallCreate;
		chooser: Snippet;
		onclose: () => void;
	} = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	const editing = !!initial;
	// svelte-ignore state_referenced_locally
	if (initial) namespace = initial.namespace;
	// svelte-ignore state_referenced_locally
	const opener = inventory.projectOf(namespace);
	const places = $derived(inventory.namespaces);
	$effect(() => {
		const list = places;
		untrack(() => {
			if (!editing && !list.includes(namespace)) namespace = list[0] ?? '';
		});
	});
	const project = $derived(inventory.projectOf(namespace));

	type Row = {
		action: 'Allow' | 'Deny';
		dest: 'cidr' | 'dns';
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
	};
	const rules = rowList<Row>(
		() => ({ action: 'Allow', dest: 'cidr', value: '', proto: 'TCP', port: null }),
		// svelte-ignore state_referenced_locally
		initial?.rules.map((r) => {
			const p = r.ports?.[0];
			return {
				action: r.action,
				dest: r.dnsName ? ('dns' as const) : ('cidr' as const),
				value: r.dnsName ?? r.cidr ?? '',
				proto: p?.protocol ?? 'TCP',
				port: p?.port ?? null,
			};
		}),
	);
	// One set per namespace: a new one where a set exists replaces it.
	const existing = $derived(
		inventory.policies.find((p) => p.kind === 'gateway' && p.namespace === namespace),
	);

	// A namespace holds one set: adding where one exists changes it.
	const gate = $derived(
		objectGate(
			{ resource: 'egressfirewall', namespace },
			editing || existing ? 'update' : 'create',
		),
	);
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!namespace) m.push('Pick a project and namespace');
		else if (!gate.ok) m.push(gate.why);
		if (!rules.rows.length) m.push('Add a rule');
		else if (rules.rows.some((r) => !r.value.trim())) m.push('Every rule needs a destination');
		return m;
	});
	const request = $derived.by((): EgressFirewallCreate => ({
		namespace,
		rules: rules.rows.map((r) => {
			const rule: EgressFirewallRule = { action: r.action };
			if (r.dest === 'cidr') rule.cidr = r.value.trim();
			else rule.dnsName = r.value.trim();
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			return rule;
		}),
	}));

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.createEgressFirewall(req))) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{@render chooser()}
		<div class="flex min-w-0 flex-col gap-1.5 sm:w-1/2">
			<label class={LABEL} for="or-where">Project and namespace</label>
			{#if editing}
				<input id="or-where" class="{FIELD} font-mono" value="{project} / {namespace}" disabled />
			{:else}
				<PlacePicker id="or-where" bind:value={namespace} namespaces={places} first={opener} />
				<span class={HELP}
					>{project === opener && opener ? 'Where you were. ' : ''}Search {plural(
						new Set(places.map((n) => inventory.projectOf(n))).size,
						'project',
					)}.</span
				>
			{/if}
		</div>

		<div class="flex flex-col gap-2">
			<span class={LABEL}>Where its VMs may go, first match wins</span>
			{#each rules.rows as r, i (i)}
				<div
					class="flex flex-wrap items-center gap-2 rounded-md border border-line px-3 py-2 text-[13px] text-ink-soft"
				>
					<select
						class="{SMALL} font-medium {r.action === 'Deny' ? 'text-danger-ink' : 'text-ok-ink'}"
						bind:value={r.action}
						aria-label="Rule {i + 1} action"
					>
						<option value="Allow">Allow</option>
						<option value="Deny">Deny</option>
					</select>
					to
					<select class={SMALL} bind:value={r.dest} aria-label="Rule {i + 1} destination kind">
						<option value="cidr">addresses</option>
						<option value="dns">the name</option>
					</select>
					<input
						class="{SMALL} w-40 font-mono"
						bind:value={r.value}
						placeholder={r.dest === 'cidr' ? '0.0.0.0/0' : 'api.example.com'}
						aria-label="Rule {i + 1} destination"
					/>
					on
					<select class={SMALL} bind:value={r.proto} aria-label="Rule {i + 1} protocol">
						<option>TCP</option><option>UDP</option><option>SCTP</option>
					</select>
					port
					<input
						type="number"
						min="1"
						max="65535"
						class="{SMALL} w-20 font-mono"
						bind:value={r.port}
						placeholder="any"
						aria-label="Rule {i + 1} port"
					/>
					<button
						type="button"
						onclick={() => rules.remove(i)}
						disabled={rules.rows.length === 1}
						aria-label="Remove rule {i + 1}"
						class="ml-auto flex size-8 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong hover:text-danger disabled:opacity-40"
						><X size={15} /></button
					>
				</div>
			{/each}
			<div><Button variant="link" onclick={rules.add}><Plus size={14} /> Add a rule</Button></div>
		</div>
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path={stagePath('egressFirewall', request)}
			{project}
			edit={editing || !!existing}
			merges={editing || existing ? 'Changed when the pull request merges.' : undefined}
			preview={missing.length ? null : { kind: 'egressFirewall', body: request }}
			backing="EgressFirewall"
		>
			{#snippet consequences()}
				{#if existing && !editing}
					<Consequence tone="warn"
						>Replaces the outbound rules {namespace} has now: a namespace keeps one set.</Consequence
					>
				{/if}
				<Consequence tone="info"
					>Checked in order for VMs in {namespace || 'the namespace'}; anything no rule matches
					still goes out.</Consequence
				>
				<Consequence tone="ok">Nothing restarts.</Consequence>
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter
			label={editing ? 'Stage changes' : 'Stage rules'}
			{missing}
			busy={op.busy}
			onsubmit={stage}
			oncancel={onclose}
		/>
	{/snippet}
</SheetBody>
