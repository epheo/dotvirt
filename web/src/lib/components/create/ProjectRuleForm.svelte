<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { Plus, X } from '@lucide/svelte';
	import { api, type NetworkPolicyCreate, type PolicyRule } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { rowList } from '#lib/rowlist.svelte.js';
	import { objectGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Button from '../Button.svelte';
	import Consequence from '../Consequence.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, SMALL, plural, stagePath, useSheet } from './sheet.svelte.js';

	// A project rule: the VMs it protects (by label, or every VM in the
	// namespace) and who may reach them, one sentence per source. Protecting
	// VMs refuses every other inbound connection to them; that is the
	// consequence said before staging.
	let {
		namespace = $bindable(),
		initial,
		chooser,
		onclose,
	}: {
		namespace: string;
		initial?: NetworkPolicyCreate;
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

	// seeded marks a source read back from git: an empty one there is a
	// deliberate allow-all, where an untouched blank row in a new rule is not.
	type Row = {
		key: string;
		value: string;
		proto: 'TCP' | 'UDP' | 'SCTP';
		port: number | null;
		seeded?: boolean;
	};
	// svelte-ignore state_referenced_locally
	const [k0, v0] = Object.entries(initial?.appliedTo ?? {})[0] ?? ['', ''];
	// svelte-ignore state_referenced_locally
	let name = $state(initial?.name ?? '');
	let appliedKey = $state(k0);
	let appliedValue = $state(v0);
	const rules = rowList<Row>(
		() => ({ key: '', value: '', proto: 'TCP', port: null }),
		// svelte-ignore state_referenced_locally
		initial?.ingress?.map((r) => {
			const [key, value] = Object.entries(r.from?.[0] ?? {})[0] ?? ['', ''];
			const p = r.ports?.[0];
			return { key, value, proto: p?.protocol ?? 'TCP', port: p?.port ?? null, seeded: true };
		}),
	);

	const members = $derived(
		inventory.allVMs.filter(
			(v) =>
				v.namespace === namespace &&
				(!appliedKey.trim() || v.labels?.[appliedKey.trim()] === appliedValue.trim()),
		),
	);
	const nameTaken = $derived(
		!editing &&
			inventory.policies.some(
				(p) => p.kind === 'dfw' && p.namespace === namespace && p.name === name,
			),
	);

	const gate = $derived(
		objectGate({ resource: 'networkpolicy', namespace }, editing ? 'update' : 'create'),
	);
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name the rule');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		else if (nameTaken) m.push(`${name} is taken in ${namespace}`);
		if (!namespace) m.push('Pick a project and namespace');
		else if (!gate.ok) m.push(gate.why);
		return m;
	});
	const request = $derived.by((): NetworkPolicyCreate => {
		const ingress: PolicyRule[] = [];
		for (const r of rules.rows) {
			const rule: PolicyRule = {};
			if (r.key.trim()) rule.from = [{ [r.key.trim()]: r.value.trim() }];
			if (r.port != null) rule.ports = [{ protocol: r.proto, port: r.port }];
			// A wholly empty row would allow everything; a seeded one is what git says.
			if (rule.from || rule.ports || r.seeded) ingress.push(rule);
		}
		const req: NetworkPolicyCreate = { name, namespace };
		if (appliedKey.trim()) req.appliedTo = { [appliedKey.trim()]: appliedValue.trim() };
		if (ingress.length) req.ingress = ingress;
		return req;
	});
	const sources = $derived(request.ingress?.length ?? 0);

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.createNetworkPolicy(req))) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{@render chooser()}
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="pr-name">Name</label>
				<input
					id="pr-name"
					class="{FIELD} font-mono font-medium"
					bind:value={name}
					disabled={editing}
					placeholder="allow-web-to-db"
					autocomplete="off"
					spellcheck="false"
					data-autofocus
				/>
				{#if name && (!validName(name) || nameTaken)}
					<span class="{HELP} text-warn-ink!"
						>{nameTaken ? `Taken in ${namespace}.` : NAME_HINT}</span
					>
				{/if}
			</div>
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="pr-where">Project and namespace</label>
				{#if editing}
					<input id="pr-where" class="{FIELD} font-mono" value="{project} / {namespace}" disabled />
				{:else}
					<PlacePicker id="pr-where" bind:value={namespace} namespaces={places} first={opener} />
					<span class={HELP}
						>{project === opener && opener ? 'Where you were. ' : ''}Search {plural(
							new Set(places.map((n) => inventory.projectOf(n))).size,
							'project',
						)}.</span
					>
				{/if}
			</div>
		</div>

		<div class="flex flex-col gap-1.5">
			<span class={LABEL} id="pr-protects">It protects</span>
			<div
				class="flex flex-wrap items-center gap-2 text-[13px] text-ink-soft"
				role="group"
				aria-labelledby="pr-protects"
			>
				VMs labeled
				<input
					class="{SMALL} w-28 font-mono"
					bind:value={appliedKey}
					placeholder="app"
					aria-label="Label key"
				/>
				=
				<input
					class="{SMALL} w-28 font-mono"
					bind:value={appliedValue}
					placeholder="db"
					aria-label="Label value"
				/>
				<span class="text-ink-faint"
					>Leave empty for every VM in {namespace || 'the namespace'}.</span
				>
			</div>
			<span class={HELP}
				>{plural(members.length, 'VM')} match{members.length === 1 ? 'es' : ''}{members.length
					? `: ${members
							.slice(0, 4)
							.map((v) => v.name)
							.join(', ')}${members.length > 4 ? `, and ${members.length - 4} more` : ''}`
					: ''}.</span
			>
		</div>

		<div class="flex flex-col gap-2">
			<span class={LABEL}>Who may reach them</span>
			{#each rules.rows as r, i (i)}
				<div
					class="flex flex-wrap items-center gap-2 rounded-md border border-line px-3 py-2 text-[13px] text-ink-soft"
				>
					Allow VMs labeled
					<input
						class="{SMALL} w-24 font-mono"
						bind:value={r.key}
						placeholder="app"
						aria-label="Source {i + 1} label key"
					/>
					=
					<input
						class="{SMALL} w-24 font-mono"
						bind:value={r.value}
						placeholder="web"
						aria-label="Source {i + 1} label value"
					/>
					on
					<select class={SMALL} bind:value={r.proto} aria-label="Source {i + 1} protocol">
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
						aria-label="Source {i + 1} port"
					/>
					<button
						type="button"
						onclick={() => rules.remove(i)}
						disabled={rules.rows.length === 1}
						aria-label="Remove source {i + 1}"
						class="ml-auto flex size-8 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong hover:text-danger disabled:opacity-40"
						><X size={15} /></button
					>
				</div>
			{/each}
			<div>
				<Button variant="link" onclick={rules.add}><Plus size={14} /> Add a source</Button>
			</div>
		</div>
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path={stagePath('networkPolicy', request)}
			{project}
			edit={editing}
			merges={editing ? 'Changed when the pull request merges.' : undefined}
			preview={missing.length ? null : { kind: 'networkPolicy', body: request }}
			backing="NetworkPolicy"
		>
			{#snippet consequences()}
				<Consequence tone="info"
					>Applies to {plural(members.length, 'VM')} in {namespace || 'the namespace'}.</Consequence
				>
				<Consequence tone="warn"
					>{sources
						? `Only the ${plural(sources, 'source')} above may reach them; every other inbound connection is refused.`
						: 'No source listed: every inbound connection to them is refused.'}</Consequence
				>
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
