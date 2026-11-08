<script lang="ts">
	import { api, type EgressIPCreate, type ExternalRouteCreate } from '#lib/api.js';
	import { splitList } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { CLUSTER_NS, objectGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui, type Tier0Initial } from '#lib/state/ui.svelte.js';
	import { NAME_HINT, validIP, validName } from '#lib/validate.js';
	import Consequence from '../Consequence.svelte';
	import PickList from './PickList.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, plural, stagePath, useSheet } from './sheet.svelte.js';

	// How a project's traffic leaves the cluster: a fixed outbound IP pins
	// the source address it leaves from; an external route sends it through
	// next hops you name. Both are cluster scoped, so they land in the
	// platform repo.
	let {
		namespace = $bindable(),
		initial,
		onclose,
	}: { namespace: string; initial?: Tier0Initial; onclose: () => void } = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	const editing = !!initial;
	// svelte-ignore state_referenced_locally
	let kind = $state<'snat' | 'route'>(initial?.kind ?? 'snat');
	// svelte-ignore state_referenced_locally
	let name = $state(initial?.spec.name ?? '');
	// svelte-ignore state_referenced_locally
	let ips = $state(
		(initial?.kind === 'snat' ? initial.spec.egressIPs : initial?.spec.nextHops)?.join(' ') ?? '',
	);
	// svelte-ignore state_referenced_locally
	let selectedNs = $state<string[]>(initial?.spec.namespaces ?? (namespace ? [namespace] : []));

	const places = $derived(inventory.repoNamespaces);
	let nsQuery = $state('');
	const nsItems = $derived(
		places.filter(
			(ns) =>
				!nsQuery ||
				`${inventory.projectOf(ns)} / ${ns}`.toLowerCase().includes(nsQuery.trim().toLowerCase()),
		),
	);
	const list = $derived(splitList(ips));
	const bad = $derived(list.filter((ip) => !validIP(ip)));
	const projects = $derived(new Set(selectedNs.map((ns) => inventory.projectOf(ns))).size);

	const gate = $derived(
		objectGate(
			{ resource: kind === 'snat' ? 'egressip' : 'externalroute', namespace: CLUSTER_NS },
			editing ? 'update' : 'create',
		),
	);
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name it');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		if (!list.length) m.push(kind === 'snat' ? 'Add an outbound IP' : 'Add a next hop');
		else if (bad.length) m.push(`${bad[0]} is not an IP address`);
		if (!selectedNs.length) m.push('Pick the projects it serves');
		if (!gate.ok) m.push(gate.why);
		return m;
	});
	const egressBody = $derived<EgressIPCreate>({ name, egressIPs: list, namespaces: selectedNs });
	const routeBody = $derived<ExternalRouteCreate>({ name, namespaces: selectedNs, nextHops: list });

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const run =
			kind === 'snat'
				? () => api.createEgressIP(egressBody)
				: () => api.createExternalRoute(routeBody);
		if (await op.run(run)) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{#if !editing}
			<fieldset class="min-w-0">
				<legend class="mb-2 {LABEL}">How its traffic leaves</legend>
				<div class="flex flex-col overflow-hidden rounded-lg border border-line">
					{#each [{ v: 'snat', t: 'Fixed outbound IP', s: 'Its VMs leave the cluster from addresses you pick.' }, { v: 'route', t: 'External route', s: 'Its traffic goes out through next hops you name.' }] as o (o.v)}
						{@const on = kind === o.v}
						<label
							class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] items-start gap-x-3 border-t border-line-soft px-3.5 py-3 first:border-t-0 {on
								? 'bg-select-soft shadow-[inset_3px_0_0_var(--color-accent)]'
								: 'hover:bg-inset'}"
						>
							<input
								type="radio"
								name="ob-kind"
								value={o.v}
								bind:group={kind}
								class="mt-0.5 size-4 accent-accent"
							/>
							<span class="flex min-w-0 flex-wrap items-baseline gap-x-2.5">
								<b class="font-bold text-ink">{o.t}</b>
								<span class="text-[13px] text-ink-muted">{o.s}</span>
							</span>
						</label>
					{/each}
				</div>
			</fieldset>
		{/if}
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="ob-name">Name</label>
				<input
					id="ob-name"
					class="{FIELD} font-mono font-medium"
					bind:value={name}
					disabled={editing}
					placeholder={kind === 'snat' ? 'team-web-out' : 'team-web-gw'}
					autocomplete="off"
					spellcheck="false"
					data-autofocus
				/>
				{#if name && !validName(name)}<span class="{HELP} text-warn-ink!">{NAME_HINT}</span>{/if}
			</div>
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="ob-ips">{kind === 'snat' ? 'Outbound IPs' : 'Next hops'}</label>
				<input
					id="ob-ips"
					class="{FIELD} font-mono"
					bind:value={ips}
					placeholder={kind === 'snat' ? '192.0.2.10 192.0.2.11' : '10.0.0.1'}
					autocomplete="off"
				/>
				<span class="{HELP} {bad.length ? 'text-warn-ink!' : ''}"
					>{bad.length
						? `${bad[0]} is not an IP address.`
						: 'Separate several with spaces or commas.'}</span
				>
			</div>
		</div>
		<PickList
			label="Projects it serves"
			bind:query={nsQuery}
			multiple
			items={nsItems}
			key={(ns) => ns}
			selected={(ns) => selectedNs.includes(ns)}
			ontoggle={(ns, on) =>
				(selectedNs = on ? [...selectedNs, ns] : selectedNs.filter((x) => x !== ns))}
			note="{selectedNs.length} picked in {plural(
				projects,
				'project',
			)}. Search {places.length} namespaces."
			limit={8}
		>
			{#snippet row(ns)}
				<span class="font-mono text-[13px] text-ink">{inventory.projectOf(ns)} / {ns}</span>
			{/snippet}
		</PickList>
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path={kind === 'snat'
				? stagePath('egressIP', egressBody)
				: stagePath('externalRoute', routeBody)}
			project="platform"
			edit={editing}
			merges={editing ? 'Changed when the pull request merges.' : undefined}
			preview={missing.length
				? null
				: kind === 'snat'
					? { kind: 'egressIP', body: egressBody }
					: { kind: 'externalRoute', body: routeBody }}
			backing={kind === 'snat' ? 'EgressIP' : 'AdminPolicyBasedExternalRoute'}
		>
			{#snippet consequences()}
				{#if list.length && selectedNs.length}
					<Consequence tone="info"
						>{kind === 'snat'
							? `VMs in ${plural(projects, 'project')} leave the cluster from ${list[0]}${list.length > 1 ? ` and ${list.length - 1} more` : ''}.`
							: `Traffic from ${plural(projects, 'project')} goes out through ${list[0]}${list.length > 1 ? ` and ${list.length - 1} more` : ''}.`}</Consequence
					>
				{/if}
				{#if kind === 'snat'}
					<Consequence>The addresses must be free on the hosts' network.</Consequence>
				{/if}
				<Consequence tone="ok">Nothing restarts.</Consequence>
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter
			label={editing ? 'Stage changes' : kind === 'snat' ? 'Stage outbound IP' : 'Stage route'}
			{missing}
			busy={op.busy}
			onsubmit={stage}
			oncancel={onclose}
		/>
	{/snippet}
</SheetBody>
