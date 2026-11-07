<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { Plus, X } from '@lucide/svelte';
	import { api, type AdminNetworkPolicyCreate, type AdminPolicyRule } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { rowList } from '#lib/rowlist.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Button from '../Button.svelte';
	import Consequence from '../Consequence.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, SMALL, stagePath, useSheet } from './sheet.svelte.js';

	// A cluster rule: priority ordered, over every project's own rules; Pass
	// hands the decision back to them. The cluster default sits under every
	// project rule instead. Platform tier, so it lands in the platform repo.
	let {
		initial,
		chooser,
		onclose,
	}: { initial?: AdminNetworkPolicyCreate; chooser: Snippet; onclose: () => void } = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	const editing = !!initial;

	// seeded marks a row read back from git: an Allow-from-all there is a
	// declared rule, where an untouched default row in a new one is not.
	type Row = {
		action: 'Allow' | 'Deny' | 'Pass';
		key: string;
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
		seeded?: boolean;
	};
	// svelte-ignore state_referenced_locally
	let tier = $state<'policy' | 'baseline'>(initial?.baseline ? 'baseline' : 'policy');
	const baseline = $derived(tier === 'baseline');
	// svelte-ignore state_referenced_locally
	const [k0, v0] = Object.entries(initial?.subject ?? {})[0] ?? ['', ''];
	// svelte-ignore state_referenced_locally
	let name = $state(initial?.name ?? '');
	// svelte-ignore state_referenced_locally
	let priority = $state<number | null>(initial ? (initial.priority ?? 0) : 10);
	let prioTouched = false;
	let subjKey = $state(k0);
	let subjValue = $state(v0);
	const rules = rowList<Row>(
		() => ({ action: 'Allow', key: '', value: '', proto: 'TCP', port: null }),
		// svelte-ignore state_referenced_locally
		initial?.ingress?.map((r) => {
			const [key, value] = Object.entries(r.peers[0] ?? {})[0] ?? ['', ''];
			const p = r.ports?.[0];
			return {
				action: r.action,
				key,
				value,
				proto: p?.protocol ?? 'TCP',
				port: p?.port ?? null,
				seeded: true,
			};
		}),
	);

	const above = $derived(
		inventory.policies
			.filter((p) => p.kind === 'admin' && p.name !== name && (p.priority ?? 0) < (priority ?? 0))
			.sort((a, b) => (a.priority ?? 0) - (b.priority ?? 0)),
	);

	// Two cluster rules may not share a priority: the backend refuses it, so
	// the form names the rule holding it first.
	const holder = $derived(
		baseline
			? undefined
			: inventory.policies.find(
					(p) => p.kind === 'admin' && p.name !== name && p.priority === priority,
				),
	);
	// The nearest free priority, for the hint beside a taken one.
	const freePriority = $derived.by(() => {
		const used = new Set(
			inventory.policies
				.filter((p) => p.kind === 'admin' && p.name !== name)
				.map((p) => p.priority),
		);
		for (let d = 0; d < 100; d++)
			for (const v of [(priority ?? 0) + d, (priority ?? 0) - d])
				if (v >= 0 && v <= 99 && !used.has(v)) return v;
		return null;
	});

	// A new rule starts on the free priority nearest 10.
	$effect(() => {
		const free = freePriority;
		untrack(() => {
			if (!editing && !prioTouched && holder && free != null) priority = free;
		});
	});

	const missing = $derived.by(() => {
		if (baseline) return [];
		const m: string[] = [];
		if (!name) m.push('Name the rule');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		if (priority == null || priority < 0 || priority > 99) m.push('Set a priority from 0 to 99');
		else if (holder) m.push(`Priority ${priority} is taken by ${holder.name}`);
		return m;
	});
	const request = $derived.by((): AdminNetworkPolicyCreate => {
		const ingress: AdminPolicyRule[] = [];
		for (const r of rules.rows) {
			// An untouched default row would ship "Allow from every project".
			if (r.action === 'Allow' && !r.key.trim() && r.port == null && !r.seeded) continue;
			const rule: AdminPolicyRule = {
				action: r.action,
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
		return req;
	});

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.createAdminNetworkPolicy(req))) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{@render chooser()}
		{#if !editing}
			<div class="flex gap-4 text-sm" role="radiogroup" aria-label="Cluster rule tier">
				{#each [{ v: 'policy', t: 'A cluster rule', s: 'priority ordered' }, { v: 'baseline', t: 'The cluster default', s: 'under every project rule' }] as o (o.v)}
					<label class="flex cursor-pointer items-center gap-2">
						<input
							type="radio"
							name="cr-tier"
							value={o.v}
							bind:group={tier}
							class="size-4 accent-accent"
						/>
						<span class="text-ink">{o.t}</span><span class="text-xs text-ink-faint">{o.s}</span>
					</label>
				{/each}
			</div>
		{/if}
		{#if !baseline}
			<div class="grid grid-cols-1 gap-4 sm:grid-cols-[minmax(0,1fr)_140px]">
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="cr-name">Name</label>
					<input
						id="cr-name"
						class="{FIELD} font-mono font-medium"
						bind:value={name}
						disabled={editing}
						placeholder="deny-lateral"
						autocomplete="off"
						spellcheck="false"
						data-autofocus
					/>
					{#if name && !validName(name)}<span class="{HELP} text-warn-ink!">{NAME_HINT}</span>{/if}
				</div>
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="cr-prio">Priority</label>
					<input
						id="cr-prio"
						type="number"
						min="0"
						max="99"
						class="{FIELD} font-mono {holder ? 'border-warn! ring-1 ring-warn' : ''}"
						bind:value={priority}
						oninput={() => (prioTouched = true)}
					/>
					{#if holder}
						<span class="{HELP} text-warn-ink!"
							>Taken by {holder.name}.{#if freePriority != null}
								<button
									type="button"
									class="text-accent-ink hover:underline"
									onclick={() => (priority = freePriority)}>Use {freePriority}</button
								>{/if}</span
						>
					{:else}
						<span class={HELP}>0 to 99. Lower runs first.</span>
					{/if}
				</div>
			</div>
		{/if}

		<div class="flex flex-col gap-1.5">
			<span class={LABEL} id="cr-subject">It applies to</span>
			<div
				class="flex flex-wrap items-center gap-2 text-[13px] text-ink-soft"
				role="group"
				aria-labelledby="cr-subject"
			>
				projects labeled
				<input
					class="{SMALL} w-28 font-mono"
					bind:value={subjKey}
					placeholder="tier"
					aria-label="Label key"
				/>
				=
				<input
					class="{SMALL} w-28 font-mono"
					bind:value={subjValue}
					placeholder="prod"
					aria-label="Label value"
				/>
				<span class="text-ink-faint">Leave empty for every project.</span>
			</div>
		</div>

		<div class="flex flex-col gap-2">
			<span class={LABEL}>Traffic in, in order</span>
			{#each rules.rows as r, i (i)}
				<div
					class="flex flex-wrap items-center gap-2 rounded-md border border-line px-3 py-2 text-[13px] text-ink-soft"
				>
					<select
						class="{SMALL} font-medium {r.action === 'Deny'
							? 'text-danger-ink'
							: r.action === 'Allow'
								? 'text-ok-ink'
								: ''}"
						bind:value={r.action}
						aria-label="Rule {i + 1} action"
					>
						<option value="Allow">Allow</option>
						<option value="Deny">Deny</option>
						{#if !baseline}<option value="Pass">Pass</option>{/if}
					</select>
					from projects labeled
					<input
						class="{SMALL} w-24 font-mono"
						bind:value={r.key}
						placeholder="any"
						aria-label="Rule {i + 1} label key"
					/>
					=
					<input
						class="{SMALL} w-24 font-mono"
						bind:value={r.value}
						aria-label="Rule {i + 1} label value"
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
			path={stagePath('adminNetworkPolicy', request)}
			project="platform"
			edit={editing}
			merges={editing ? 'Changed when the pull request merges.' : undefined}
			preview={missing.length ? null : { kind: 'adminNetworkPolicy', body: request }}
			backing={baseline ? 'BaselineAdminNetworkPolicy' : 'AdminNetworkPolicy'}
		>
			{#snippet consequences()}
				<Consequence tone="info"
					>Applies to {subjKey.trim()
						? `projects labeled ${subjKey.trim()}=${subjValue.trim()}`
						: 'every project'}.</Consequence
				>
				{#if baseline}
					<Consequence>Decides only what no project rule decides.</Consequence>
				{:else}
					<Consequence tone="warn"
						>Project rules cannot override it; Pass hands a match back to them.</Consequence
					>
					{#if above.length}
						<Consequence
							>{above.length === 1
								? `${above[0].name} (priority ${above[0].priority}) runs before it.`
								: `${above.length} cluster rules run before it.`}</Consequence
						>
					{/if}
				{/if}
				<Consequence tone="ok">Nothing restarts.</Consequence>
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter
			label={editing ? 'Stage changes' : 'Stage rule'}
			{missing}
			busy={op.busy}
			onsubmit={stage}
			oncancel={onclose}
		/>
	{/snippet}
</SheetBody>
