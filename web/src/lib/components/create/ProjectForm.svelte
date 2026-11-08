<script lang="ts">
	import { untrack } from 'svelte';
	import { TriangleAlert } from '@lucide/svelte';
	import { api, type ProjectCreate } from '#lib/api.js';
	import { splitList } from '#lib/format.js';
	import { action } from '#lib/resource.svelte.js';
	import { clusterGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { nearestFree, overlapping, suggestSubnet } from '#lib/subnets.js';
	import { NAME_HINT, validCIDR, validName } from '#lib/validate.js';
	import Consequence from '../Consequence.svelte';
	import StageChip from '../StageChip.svelte';
	import SheetBody from './SheetBody.svelte';
	import SheetFooter from './SheetFooter.svelte';
	import StagesPanel from './StagesPanel.svelte';
	import { FIELD, HELP, LABEL, useSheet } from './sheet.svelte.js';

	// A project: a team space with its own repo. The repo is made right away
	// (a repo is not a manifest); its first namespace, an owners grant and its
	// default network stage into the platform repo. Adopting brings an
	// existing namespace in, as a new project or into one that has a repo.
	let { adopt = '', onclose }: { adopt?: string; onclose: () => void } = $props();

	const sheet = useSheet();
	// svelte-ignore state_referenced_locally
	let name = $state(adopt);
	// svelte-ignore state_referenced_locally
	let namespace = $state(adopt);
	let owners = $state('');
	let joinProject = $state('');
	const joinable = $derived(inventory.projects.filter((p) => p.repo).map((p) => p.name));
	const joining = $derived(!!adopt && !!joinProject);
	// An adopted namespace already runs on whatever networking it has: a new
	// default network could break its VMs, so it is not offered.
	// svelte-ignore state_referenced_locally
	let withNetwork = $state(!adopt);
	let netName = $state('');
	let netTouched = false;
	// svelte-ignore state_referenced_locally
	let nsTouched = !!adopt;
	$effect(() => {
		const n = name;
		untrack(() => {
			if (!nsTouched) namespace = n;
		});
	});
	$effect(() => {
		const ns = namespace;
		untrack(() => {
			if (!netTouched) netName = ns ? `${ns}-net` : '';
		});
	});

	const networks = $derived(inventory.networks);
	const taken = $derived(networks.flatMap((n) => n.subnets ?? []));
	const suggestion = $derived(
		suggestSubnet(networks.filter((n) => n.kind === 'default' && n.subnets?.length).at(-1), taken),
	);
	let subnet = $state('');
	let subnetTouched = false;
	$effect(() => {
		const s = suggestion?.cidr ?? '';
		untrack(() => {
			if (!subnetTouched) subnet = s;
		});
	});
	const clash = $derived(
		withNetwork && validCIDR(subnet.trim()) ? overlapping(subnet.trim(), networks) : [],
	);
	const fixes = $derived(
		[
			...new Set([suggestion?.cidr, clash.length ? nearestFree(subnet.trim(), taken)[0] : '']),
		].filter((c): c is string => !!c && !overlapping(c, networks).length),
	);
	const exists = $derived(!adopt && inventory.projects.some((p) => p.name === name));

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!joining) {
			if (!name) m.push('Name the project');
			else if (!validName(name)) m.push('The name needs lowercase letters, digits and -');
			else if (exists) m.push(`${name} exists: adopt it from its menu instead`);
		}
		if (!clusterGate.namespaceCreate().ok) m.push(clusterGate.namespaceCreate().why);
		if (!namespace) m.push('Name its first namespace');
		else if (!validName(namespace)) m.push('The namespace needs lowercase letters, digits and -');
		if (splitList(owners).length && !clusterGate.grantAdmin().ok)
			m.push(clusterGate.grantAdmin().why);
		if (withNetwork) {
			if (!clusterGate.vmNetwork().ok) m.push(clusterGate.vmNetwork().why);
			if (!netName) m.push('Name its network');
			if (!subnet.trim()) m.push('Its network needs a subnet');
			else if (!validCIDR(subnet.trim())) m.push('The subnet needs CIDR form, like 10.40.0.0/16');
		}
		return m;
	});
	const request = $derived.by((): ProjectCreate => {
		const req: ProjectCreate = { name, namespace };
		const o = splitList(owners);
		if (o.length) req.owners = o;
		if (withNetwork) req.vmNetwork = { name: netName, subnet: subnet.trim() };
		return req;
	});

	const op = action();
	async function stage() {
		if (missing.length || op.busy) return;
		const req = request;
		const run = joining
			? () => api.createNamespace({ name: namespace, project: joinProject })
			: () => api.createProject(req);
		if (await op.run(run)) {
			ui.toastStaged();
			onclose();
		}
	}
	sheet.submit = stage;
</script>

<SheetBody error={op.error}>
	{#snippet form()}
		{#if adopt && joinable.length}
			<div class="flex flex-col gap-1.5 sm:w-1/2">
				<label class={LABEL} for="pj-join">Bring {adopt} into</label>
				<select id="pj-join" class={FIELD} bind:value={joinProject}>
					<option value="">A new project</option>
					{#each joinable as p (p)}<option value={p}>{p}</option>{/each}
				</select>
			</div>
		{/if}
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			{#if !joining}
				<div class="flex min-w-0 flex-col gap-1.5">
					<label class={LABEL} for="pj-name">Name</label>
					<input
						id="pj-name"
						class="{FIELD} font-mono font-medium"
						bind:value={name}
						placeholder="team-c"
						autocomplete="off"
						spellcheck="false"
						data-autofocus
					/>
					<span class="{HELP} {name && (!validName(name) || exists) ? 'text-warn-ink!' : ''}"
						>{name && !validName(name)
							? NAME_HINT
							: exists
								? 'That project exists.'
								: 'Its repo takes the same name.'}</span
					>
				</div>
			{/if}
			<div class="flex min-w-0 flex-col gap-1.5">
				<label class={LABEL} for="pj-ns">{adopt ? 'Namespace' : 'First namespace'}</label>
				<input
					id="pj-ns"
					class="{FIELD} font-mono"
					bind:value={namespace}
					oninput={() => (nsTouched = true)}
					disabled={!!adopt}
					autocomplete="off"
					spellcheck="false"
				/>
				<span class={HELP}
					>{adopt ? 'It exists already.' : 'Follows the name until you change it.'}</span
				>
			</div>
		</div>
		{#if !joining}
			<div class="flex flex-col gap-1.5">
				<label class={LABEL} for="pj-owners">Owners</label>
				<input id="pj-owners" class={FIELD} bind:value={owners} placeholder="alice bob" />
				<span class={HELP}>They can change anything in it. Separate names with spaces.</span>
			</div>
		{/if}
		{#if !adopt}
			<label class="flex cursor-pointer items-center gap-2 text-sm text-ink">
				<input type="checkbox" class="size-4 accent-accent" bind:checked={withNetwork} />
				Give it a default network its VMs start on
			</label>
			{#if withNetwork}
				<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
					<div class="flex min-w-0 flex-col gap-1.5">
						<label class={LABEL} for="pj-net">Network name</label>
						<input
							id="pj-net"
							class="{FIELD} font-mono"
							bind:value={netName}
							oninput={() => (netTouched = true)}
						/>
					</div>
					<div class="flex min-w-0 flex-col gap-1.5">
						<label class={LABEL} for="pj-subnet">Subnet</label>
						<input
							id="pj-subnet"
							class="{FIELD} font-mono {clash.length ? 'border-warn! ring-1 ring-warn' : ''}"
							bind:value={subnet}
							oninput={() => (subnetTouched = true)}
						/>
						{#if suggestion}<span class={HELP}
								><span
									>Suggested <span class="font-mono text-ink-soft">{suggestion.cidr}</span>. Why: {suggestion.reason}.</span
								></span
							>{/if}
					</div>
				</div>
				{#if clash.length}
					<div
						class="flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn-soft px-3.5 py-3"
					>
						<TriangleAlert size={16} class="mt-0.5 shrink-0 text-warn-ink" />
						<div class="flex min-w-0 flex-1 flex-col gap-2">
							<span class="font-bold text-warn-ink"
								>Already used by {clash.map((n) => n.name).join(' and ')}.</span
							>
							<div class="flex flex-wrap items-center gap-2">
								{#each fixes as c, i (c)}
									<button
										type="button"
										onclick={() => {
											subnet = c;
											subnetTouched = true;
										}}
										class="h-[30px] rounded-md border px-3 text-[13px] {i === 0
											? 'border-accent bg-panel text-accent-ink'
											: 'border-line bg-panel text-ink'}"
										>Use <span class="font-mono">{c}</span></button
									>
								{/each}
								<span class="text-xs text-ink-faint">nearest free</span>
							</div>
						</div>
					</div>
				{/if}
			{/if}
		{/if}
	{/snippet}

	{#snippet aside()}
		<StagesPanel
			path="namespaces/{namespace || '<name>'}.yaml"
			project="platform"
			preview={missing.length || joining ? null : { kind: 'project', body: request }}
		>
			{#snippet extra()}
				{#if !joining && !adopt}
					<div class="flex flex-col items-start gap-1">
						<StageChip kind="now" />
						<span class="text-xs leading-4 text-ink-faint"
							>The <span class="font-mono">{name || 'project'}</span> repo is made when you stage.</span
						>
					</div>
				{/if}
			{/snippet}
			{#snippet consequences()}
				<Consequence tone="info"
					>{joining
						? `${namespace} joins ${joinProject}; its VMs show up there, ready to bring into git.`
						: adopt
							? `${namespace} becomes a project; its VMs show up there, ready to bring into git.`
							: namespace
								? `The namespace ${namespace} is created and joins ${name || 'the project'}.`
								: 'Its first namespace is created with it.'}</Consequence
				>
				{#if splitList(owners).length && !joining}
					<Consequence>{splitList(owners).join(', ')} can change anything in it.</Consequence>
				{/if}
				{#if withNetwork && !adopt}
					<Consequence tone={clash.length ? 'warn' : 'ok'}
						>{clash.length
							? `Its network overlaps ${clash.map((n) => n.name).join(' and ')}.`
							: `Its VMs start on ${netName || 'its network'}, ${subnet || 'no range yet'}.`}</Consequence
					>
				{/if}
			{/snippet}
		</StagesPanel>
	{/snippet}

	{#snippet footer()}
		<SheetFooter
			label={joining ? 'Stage namespace' : 'Stage project'}
			{missing}
			busy={op.busy}
			onsubmit={stage}
			oncancel={onclose}
		/>
	{/snippet}
</SheetBody>
