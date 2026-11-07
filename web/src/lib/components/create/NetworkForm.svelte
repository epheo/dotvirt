<script lang="ts">
	import { untrack } from 'svelte';
	import { Check, TriangleAlert } from '@lucide/svelte';
	import { api, type NetworkCreate } from '#lib/api.js';
	import { attachRef } from '#lib/networks.js';
	import { action } from '#lib/resource.svelte.js';
	import { nodeTargets } from '#lib/state/hosts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { canMeet, nearestFree, overlapping, suggestSubnet } from '#lib/subnets.js';
	import { NAME_HINT, validName } from '#lib/validate.js';
	import Card from '../Card.svelte';
	import Consequence from '../Consequence.svelte';
	import PickList from './PickList.svelte';
	import PlacePicker from './PlacePicker.svelte';
	import SetupRow from './SetupRow.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, plural, stagePath, useSheet } from './sheet.svelte.js';

	// New network: who can attach in plain words, a name, and a subnet that
	// comes suggested with its reason. A range already in use names its
	// owners and offers the nearest free ones in one click. The same form
	// edits a declared network, where only a shared network's projects move.
	let {
		namespace = $bindable(),
		initial,
		like,
		onclose,
	}: {
		namespace: string;
		initial?: NetworkCreate;
		like?: string; // attach ref of the network to fill from
		onclose: () => void;
	} = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	const editing = !!initial;
	const networks = $derived(inventory.networks);
	// svelte-ignore state_referenced_locally
	const likeNet = like ? inventory.networks.find((n) => attachRef(n) === like) : undefined;
	// svelte-ignore state_referenced_locally
	if (!namespace && likeNet?.namespace) namespace = likeNet.namespace;
	// svelte-ignore state_referenced_locally
	if (initial?.namespace) namespace = initial.namespace;

	type Who = 'project' | 'shared' | 'vlan';
	// svelte-ignore state_referenced_locally
	let who = $state<Who>((initial?.scope as Who) ?? 'project');
	// svelte-ignore state_referenced_locally
	let name = $state(initial?.name ?? '');
	// svelte-ignore state_referenced_locally
	let selectedNs = $state<string[]>(
		initial?.namespaces ?? likeNet?.namespaces ?? (namespace ? [namespace] : []),
	);
	// svelte-ignore state_referenced_locally
	let vlan = $state<number | null>(initial?.vlan ?? null);
	// svelte-ignore state_referenced_locally
	let physnet = $state(initial?.physicalNetwork ?? '');

	const places = $derived(inventory.namespaces);
	$effect(() => {
		const list = places;
		untrack(() => {
			if (who === 'project' && !editing && !list.includes(namespace)) namespace = list[0] ?? '';
		});
	});
	const project = $derived(inventory.projectOf(namespace));
	const owner = $derived(
		who === 'project'
			? project +
					(places.filter((n) => inventory.projectOf(n) === project).length > 1
						? ` / ${namespace}`
						: '')
			: '',
	);

	// ── name ───────────────────────────────────────────────────────────────
	const sameScope = $derived(
		networks.filter((n) =>
			who === 'project' ? n.scope === 'project' && n.namespace === namespace : n.scope === 'shared',
		),
	);
	const nameTaken = $derived(!editing && sameScope.some((n) => n.name === name));
	const where = $derived(who === 'project' ? project : 'the cluster');

	// ── subnet ─────────────────────────────────────────────────────────────
	// svelte-ignore state_referenced_locally
	let subnet = $state(initial?.subnets?.[0] ?? '');
	let subnetTouched = editing;
	// svelte-ignore state_referenced_locally
	let addresses = $state(!editing || !!initial?.subnets?.length);
	const others = $derived(
		networks.filter(
			(n) => !(editing && n.name === initial?.name && n.namespace === initial?.namespace),
		),
	);
	const taken = $derived(others.flatMap((n) => n.subnets ?? []));
	// The network the range sits next to: the one the sheet came from, else
	// the newest with a range in the same place.
	const near = $derived(
		likeNet ??
			others.find(
				(n) =>
					n.subnets?.length &&
					(who === 'project' ? n.namespace === namespace : n.scope === 'shared'),
			),
	);
	const suggestion = $derived(suggestSubnet(near, taken));
	$effect(() => {
		const s = suggestion?.cidr ?? '';
		untrack(() => {
			if (!subnetTouched && addresses) subnet = s;
		});
	});
	const subnetValid = $derived(!subnet.trim() || /^[0-9a-fA-F:.]+\/\d{1,3}$/.test(subnet.trim()));
	const overlap = $derived(
		addresses && subnet.trim() && subnetValid ? overlapping(subnet.trim(), others) : [],
	);
	// Only a network the new one can meet makes the overlap a problem; an
	// isolated project network elsewhere may reuse the range (subnets.canMeet).
	const self = $derived(who === 'project' ? { namespace } : { namespaces: selectedNs });
	const clash = $derived(overlap.filter((n) => canMeet(self, n)));
	const reuse = $derived(overlap.filter((n) => !canMeet(self, n)));
	const reuseText = $derived(
		`${reuse.map((n) => n.name).join(', ')} ${reuse.length === 1 ? 'uses' : 'use'} this range in another namespace. They never meet, so that is fine.`,
	);
	const freeNear = $derived(clash.length ? nearestFree(subnet.trim(), taken, 1)[0] : undefined);
	const fixes = $derived(
		[...new Set([suggestion?.cidr, freeNear].filter((c): c is string => !!c))].filter(
			(c) => !overlapping(c, others).length,
		),
	);
	function use(c: string) {
		subnet = c;
		subnetTouched = true;
	}

	// ── uplinks (VLAN) ─────────────────────────────────────────────────────
	const nodes = nodeTargets();
	const hostCount = $derived(nodes.data?.length ?? 0);
	const uplinks = $derived(inventory.uplinks.filter((u) => !u.builtin));
	const failing = (u: { policy?: string; health?: string }) =>
		u.health === 'Degraded' ||
		!!inventory.inventory?.platform?.unhealthy?.some(
			(o) => o.kind === 'NodeNetworkConfigurationPolicy' && o.name === u.policy,
		);
	const failure = (u: { policy?: string }) =>
		inventory.inventory?.platform?.unhealthy?.find((o) => o.name === u.policy)?.message ?? '';
	// The hosts a failure names, when its message lists them ("... (host-41)").
	const failingOn = (u: { policy?: string }) => /\(([^)]+)\)\s*$/.exec(failure(u))?.[1] ?? '';
	$effect(() => {
		const first = uplinks.find((u) => !failing(u)) ?? uplinks[0];
		untrack(() => {
			if (who === 'vlan' && !physnet && first) physnet = first.name;
		});
	});
	const chosenUplink = $derived(uplinks.find((u) => u.name === physnet));
	// The "Carried by" line names the uplink carrying the most VLAN networks
	// and a failing one when there is; the rest are a count.
	const vlansOn = (u: { name: string }) =>
		networks.filter((n) => n.kind === 'vlan' && n.uplink === u.name).length;
	const carriers = $derived.by(() => {
		const byUse = [...uplinks].sort((a, b) => vlansOn(b) - vlansOn(a));
		const lead = [
			...byUse.filter((u) => !failing(u)).slice(0, 1),
			...byUse.filter(failing).slice(0, 1),
		];
		return [...lead, ...byUse.filter((u) => !lead.includes(u))];
	});

	// ── projects for a shared network ──────────────────────────────────────
	let nsQuery = $state('');
	const nsItems = $derived(
		places.filter(
			(ns) =>
				!nsQuery ||
				`${inventory.projectOf(ns)} / ${ns}`.toLowerCase().includes(nsQuery.trim().toLowerCase()),
		),
	);
	const projectsPicked = $derived(new Set(selectedNs.map((ns) => inventory.projectOf(ns))).size);

	// ── the request ────────────────────────────────────────────────────────
	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Name the network');
		else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
		else if (nameTaken) m.push(`${name} is taken in ${where}`);
		if (who === 'project' && !namespace) m.push('Pick a project');
		if (who !== 'project' && !selectedNs.length) m.push('Pick the projects that attach');
		if (who === 'vlan') {
			if (!(vlan && vlan >= 1 && vlan <= 4094)) m.push('Set a VLAN id between 1 and 4094');
			if (!physnet) m.push('Pick an uplink');
		}
		if (!subnetValid) m.push('The subnet needs CIDR form, like 10.40.0.0/16');
		return m;
	});
	const request = $derived.by((): NetworkCreate => {
		const req: NetworkCreate =
			who === 'vlan'
				? {
						name,
						scope: 'vlan',
						physicalNetwork: physnet,
						vlan: vlan ?? undefined,
						namespaces: selectedNs,
					}
				: who === 'shared'
					? { name, scope: 'shared', namespaces: selectedNs }
					: { name, namespace, scope: 'project' };
		// A declared network keeps its ranges verbatim: OVN-K freezes them.
		if (editing) req.subnets = initial!.subnets;
		else if (addresses && subnet.trim()) req.subnets = [subnet.trim()];
		return req;
	});

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		if (await op.run(() => api.createNetwork(req))) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;

	let open = $state<'' | 'addresses'>('');
	const egressIP = $derived(
		inventory.policies.find(
			(p) =>
				p.kind === 'egressip' && (p.namespaces ?? []).some((ns) => selectedOrHere.includes(ns)),
		),
	);
	const selectedOrHere = $derived(who === 'project' ? [namespace] : selectedNs);

	const CHOICES: { id: Who; label: string; text: () => string }[] = [
		{
			id: 'project',
			label: 'Project network',
			text: () => `Only VMs in ${owner || 'one project'}.`,
		},
		{ id: 'shared', label: 'Shared network', text: () => 'VMs from the projects you pick.' },
		{ id: 'vlan', label: 'VLAN network', text: () => 'VMs join a VLAN on your physical network.' },
	];
	const choices = $derived(CHOICES.filter((c) => c.id === 'project' || inventory.canManage));
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{#if editing}
			<p class="text-sm text-ink-soft">
				{who === 'vlan'
					? `VLAN ${vlan} on ${physnet}`
					: who === 'shared'
						? 'Shared network'
						: 'Project network'}{initial?.subnets?.length ? `, ${initial.subnets.join(', ')}` : ''}.
				Who can attach, the range and the VLAN are fixed once it exists{who === 'project'
					? '.'
					: '; the projects it reaches can change.'}
			</p>
		{:else}
			<fieldset class="min-w-0">
				<legend class="mb-2 {LABEL}">Who can attach</legend>
				<div class="flex flex-col overflow-hidden rounded-lg border border-line">
					{#each choices as c (c.id)}
						{@const on = who === c.id}
						<label
							class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] items-start gap-x-3 gap-y-1 border-t border-line-soft px-3.5 py-3 first:border-t-0 {on
								? 'bg-select-soft shadow-[inset_3px_0_0_var(--color-accent)]'
								: 'hover:bg-inset'}"
						>
							<input
								type="radio"
								name="net-who"
								value={c.id}
								bind:group={who}
								class="mt-0.5 size-4 accent-accent"
							/>
							<span class="flex min-w-0 flex-wrap items-baseline gap-x-2.5">
								<b class="font-bold text-ink">{c.label}</b>
								<span class="text-[13px] text-ink-muted">{c.text()}</span>
							</span>
							{#if c.id === 'vlan' && uplinks.length}
								<span
									class="col-start-2 flex flex-wrap items-center gap-x-3.5 gap-y-0.5 text-xs leading-[18px] text-ink-soft"
								>
									<span class="text-ink-faint">Carried by</span>
									{#each carriers.slice(0, 2) as u (u.name)}
										<span class="inline-flex items-center gap-1.5">
											<span class="size-[7px] rounded-full {failing(u) ? 'bg-danger' : 'bg-ok'}"
											></span>
											<span class="font-mono font-medium">{u.name}</span>
											{failing(u)
												? `failing${failingOn(u) ? ` on ${failingOn(u)}` : ''}`
												: hostCount
													? `on ${u.nodeCount} of ${hostCount} hosts`
													: `on ${u.nodeCount} hosts`}
										</span>
									{/each}
									{#if carriers.length > 2}<span class="text-ink-faint"
											>and {carriers.length - 2} more</span
										>{/if}
								</span>
							{/if}
						</label>
					{/each}
				</div>
			</fieldset>

			<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="net-name">Name</label>
					<input
						id="net-name"
						class="{FIELD} font-mono font-medium"
						bind:value={name}
						autocomplete="off"
						spellcheck="false"
						placeholder="web-backend"
						data-autofocus
					/>
					{#if name}
						<span
							class="{HELP} {validName(name) && !nameTaken ? 'text-ok-ink!' : 'text-warn-ink!'}"
						>
							{#if !validName(name)}<TriangleAlert
									size={13}
								/>{NAME_HINT}{:else if nameTaken}<TriangleAlert size={13} />Taken in {where}.{:else}<Check
									size={13}
									strokeWidth={2.4}
								/>Free in {where}.{/if}
						</span>
					{/if}
				</div>
				{#if who === 'project'}
					<div class="flex min-w-0 flex-col gap-1.5">
						<label class={LABEL} for="net-where">Project</label>
						<PlacePicker
							id="net-where"
							bind:value={namespace}
							namespaces={places}
							first={project}
							short
						/>
						<span class={HELP}
							>{likeNet ? `Owner of ${likeNet.name}. ` : ''}Search {plural(
								new Set(places.map((n) => inventory.projectOf(n))).size,
								'project',
							)}.</span
						>
					</div>
				{:else if who === 'vlan'}
					<div class="flex min-w-0 flex-col gap-1.5">
						<label class={LABEL} for="net-vlan">VLAN id</label>
						<input
							id="net-vlan"
							type="number"
							min="1"
							max="4094"
							class="{FIELD} font-mono"
							bind:value={vlan}
							placeholder="101"
						/>
						<span class={HELP}>The tag your switches carry, 1 to 4094.</span>
					</div>
				{/if}
			</div>

			{#if who === 'vlan'}
				<fieldset class="min-w-0">
					<legend class="mb-2 {LABEL}">Uplink</legend>
					{#if uplinks.length}
						<div class="flex flex-col overflow-hidden rounded-md border border-line">
							{#each uplinks as u (u.name)}
								<label
									class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] gap-3 border-t border-line-soft px-3 py-2 first:border-t-0 {physnet ===
									u.name
										? 'bg-select-soft'
										: 'hover:bg-inset'}"
								>
									<input
										type="radio"
										name="net-uplink"
										value={u.name}
										bind:group={physnet}
										class="mt-0.5 size-4 accent-accent"
									/>
									<span class="min-w-0">
										<span class="font-mono text-sm font-medium text-ink">{u.name}</span>
										<span class="block text-xs text-ink-muted"
											>Bridge <span class="font-mono">{u.bridge}</span>, {u.nodeCount}
											host{u.nodeCount === 1 ? '' : 's'}{#if failing(u)}<span
													class="text-danger-ink"
													>, failing{failure(u) ? `: ${failure(u)}` : ''}</span
												>{/if}</span
										>
									</span>
								</label>
							{/each}
						</div>
					{:else}
						<p class="text-[13px] text-ink-muted">
							No uplink carries a VLAN yet.
							<button
								type="button"
								class="text-accent-ink hover:underline"
								onclick={() => (ui.modal = { kind: 'uplink' })}>Add an uplink</button
							>
						</p>
					{/if}
				</fieldset>
			{/if}
		{/if}

		{#if who !== 'project'}
			<PickList
				label="Projects that attach"
				bind:query={nsQuery}
				multiple
				items={nsItems}
				key={(ns) => ns}
				selected={(ns) => selectedNs.includes(ns)}
				ontoggle={(ns, on) =>
					(selectedNs = on ? [...selectedNs, ns] : selectedNs.filter((x) => x !== ns))}
				note="{selectedNs.length} picked in {projectsPicked} project{projectsPicked === 1
					? ''
					: 's'}. Search {places.length} namespaces."
				limit={8}
			>
				{#snippet row(ns)}
					<span class="font-mono text-[13px] text-ink">{inventory.projectOf(ns)} / {ns}</span>
				{/snippet}
			</PickList>
		{/if}

		{#if !editing && addresses}
			<div class="flex flex-col gap-1.5">
				<label class={LABEL} for="net-subnet">Subnet</label>
				<input
					id="net-subnet"
					class="{FIELD} font-mono {clash.length || !subnetValid
						? 'border-warn! ring-1 ring-warn focus:ring-warn'
						: ''}"
					bind:value={subnet}
					oninput={() => (subnetTouched = true)}
					autocomplete="off"
					spellcheck="false"
					aria-describedby="net-subnet-help net-subnet-warn net-subnet-reuse"
				/>
				<span id="net-subnet-help" class={HELP}>
					{#if !subnetValid}<span class="text-warn-ink">Use CIDR form, like 10.40.0.0/16.</span
						>{:else if suggestion}<span
							>Suggested <span class="font-mono text-ink-soft">{suggestion.cidr}</span>. Why: {suggestion.reason}.</span
						>{/if}
				</span>

				{#if clash.length}
					<div
						id="net-subnet-warn"
						class="mt-1 flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn-soft px-3.5 py-3"
					>
						<TriangleAlert size={16} class="mt-0.5 shrink-0 text-warn-ink" />
						<div class="flex min-w-0 flex-1 flex-col gap-0.5">
							<span class="font-bold text-warn-ink"
								>Already used by {#each clash as n, i (attachRef(n))}{i
										? i === clash.length - 1
											? ' and '
											: ', '
										: ''}<span class="font-mono font-medium">{n.name}</span>{/each}.</span
							>
							<span class="text-[13px] text-ink-soft"
								>Addresses would repeat on any VM or route that joins them.</span
							>
							{#if fixes.length}
								<div class="mt-2 flex flex-wrap items-center gap-2">
									{#each fixes as c, i (c)}
										<button
											type="button"
											onclick={() => use(c)}
											class="inline-flex h-[30px] items-center gap-1 rounded-md border px-3 text-[13px] {i ===
											0
												? 'border-accent bg-panel text-accent-ink hover:bg-select-soft'
												: 'border-line bg-panel text-ink hover:border-ink-faint'}"
											>Use <span class="font-mono">{c}</span></button
										>
									{/each}
									<span class="text-xs text-ink-faint">nearest free</span>
								</div>
							{/if}
						</div>
					</div>
				{/if}
				{#if reuse.length}
					<span id="net-subnet-reuse" class={HELP}>{reuseText}</span>
				{/if}
			</div>
		{/if}

		{#if !editing}
			<Card title="Set up for you" flush>
				{#snippet actions()}<span class="text-ink-faint"
						>{likeNet ? `Same as ${likeNet.name}` : 'The usual for a new network'}</span
					>{/snippet}
				<div class="flex flex-col">
					<SetupRow
						label="Addresses"
						open={open === 'addresses'}
						onopen={() => (open = 'addresses')}
						onclose={() => (open = '')}
					>
						{#snippet value()}{addresses ? 'Handed out automatically' : 'None handed out'}{/snippet}
						{#snippet sub()}{addresses
								? 'VMs keep theirs across restarts'
								: 'VMs set their own addresses'}{/snippet}
						{#snippet editor(done)}
							<div class="flex flex-col gap-2">
								{#each [{ v: true, t: 'Hand out addresses from the subnet', s: 'VMs keep theirs across restarts and live migration.' }, { v: false, t: 'None', s: 'A plain switch: VMs set their own addresses.' }] as o (o.t)}
									<label class="flex cursor-pointer items-start gap-3 text-sm">
										<input
											type="radio"
											name="net-addr"
											checked={addresses === o.v}
											onchange={() => {
												addresses = o.v;
												if (!o.v) subnet = '';
												else subnetTouched = false;
											}}
											class="mt-0.5 size-4 accent-accent"
										/>
										<span
											><span class="text-ink">{o.t}</span><span class="block text-xs text-ink-muted"
												>{o.s}</span
											></span
										>
									</label>
								{/each}
								<div>
									<button
										type="button"
										onclick={done}
										class="h-[30px] rounded-md border border-line bg-panel px-3 text-[13px] text-ink hover:border-ink-faint"
										>Done</button
									>
								</div>
							</div>
						{/snippet}
					</SetupRow>
					<SetupRow label="Outbound" onopen={() => {}} onclose={() => {}}>
						{#snippet value()}
							{#if who === 'vlan'}Onto VLAN {vlan ?? '?'} through {physnet ||
									'its uplink'}{:else}Through each VM's default network{/if}
						{/snippet}
						{#snippet sub()}
							{#if who === 'vlan'}Your physical network routes it{:else if egressIP}Leaves through
								the fixed outbound IP {egressIP.name}{:else}No fixed outbound IP{/if}
						{/snippet}
					</SetupRow>
				</div>
			</Card>
		{/if}
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path={stagePath('network', request) || 'networks/<name>.yaml'}
			project={who === 'project' ? project : 'platform'}
			edit={editing}
			merges={editing ? 'Changed when the pull request merges.' : undefined}
			preview={missing.length ? null : { kind: 'network', body: request }}
			backing={who === 'project' ? 'UserDefinedNetwork' : 'ClusterUserDefinedNetwork'}
		>
			{#snippet consequences()}
				{#if clash.length}
					<Consequence tone="warn">Overlaps {clash.map((n) => n.name).join(' and ')}.</Consequence>
				{/if}
				{#if who === 'vlan' && chosenUplink && failing(chosenUplink)}
					<Consequence tone="danger"
						>{chosenUplink.name} is failing: VMs on its broken hosts cannot reach the VLAN.</Consequence
					>
				{/if}
				<Consequence tone="ok">Nothing restarts. No VM joins until you attach one.</Consequence>
				{#if who === 'project'}
					<Consequence tone="info"
						>Shows up in the network picker of every {project} VM{owner.includes('/')
							? ` in ${namespace}`
							: ''}.</Consequence
					>
				{:else if selectedNs.length}
					<Consequence tone="info"
						>Shows up for VMs in the {projectsPicked} project{projectsPicked === 1 ? '' : 's'} you picked.</Consequence
					>
				{/if}
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter
			label={editing ? 'Stage changes' : 'Stage network'}
			{missing}
			busy={op.busy}
			onsubmit={stage}
			oncancel={onclose}
		/>
	{/snippet}
</SheetBody>
