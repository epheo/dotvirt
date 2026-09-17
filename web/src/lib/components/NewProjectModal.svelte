<script lang="ts">
	import { api, type ProjectCreate } from '$lib/api';
	import { splitList } from '$lib/format';
	import { validName, NAME_HINT, validCIDR } from '$lib/validate';
	import { inventory } from '$lib/state/inventory.svelte';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';
	import VMNetworkFields from './VMNetworkFields.svelte';

	let {
		onclose,
		adopt: adoptProp = '',
	}: {
		onclose: () => void;
		adopt?: string; // existing unlabeled namespace to bring in as a project
	} = $props();
	// svelte-ignore state_referenced_locally
	const adopt = adoptProp;

	let name = $state(adopt); // project name -> tenant repo + dotvirt.io/project
	let namespace = $state(adopt); // first namespace
	let owners = $state(''); // space/comma-separated usernames
	// An adopted namespace may join a project that already has a repo instead of
	// minting one; '' keeps the new-project path.
	let joinProject = $state('');
	const joinable = $derived(inventory.projects.filter((p) => p.repo).map((p) => p.name));
	const joining = $derived(!!adopt && !!joinProject);
	// Adoption defaults to no new VM network: the namespace already runs VMs on
	// whatever networking it has, and staging a primary UDN could break them.
	let withNetwork = $state(!adopt);
	let netName = $state('');
	let subnet = $state('');

	// The first namespace defaults to the project name until the user overrides
	// it. Adoption pins it: the whole point is bringing in THAT namespace, so a
	// project rename must not drag it along.
	let nsTouched = $state(!!adopt);
	$effect(() => {
		if (!nsTouched) namespace = name;
	});
	let netTouched = $state(false);

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!joining) {
			if (!name) m.push('Project name is required');
			else if (!validName(name)) m.push(`Project name: ${NAME_HINT}`);
		}
		if (!namespace) m.push('First namespace is required');
		else if (!validName(namespace)) m.push(`Namespace: ${NAME_HINT}`);
		if (withNetwork) {
			if (!netName) m.push('VM Network name is required');
			if (!subnet.trim()) m.push('Subnet is required for a primary network');
			else if (!validCIDR(subnet.trim())) m.push('Subnet must be a CIDR (e.g. 10.40.0.0/16)');
		}
		return m;
	});
	const valid = $derived(missing.length === 0);
	// The adopt lines stay short - the body note already carries the full story,
	// and the truncated long form read as noise.
	const summary = $derived(
		!valid
			? ''
			: joining
				? `Adopts ${namespace} into project “${joinProject}”`
				: adopt
					? `Adopts ${namespace} as project “${name}”`
					: `Creates repo “${name}”; stages namespace ${namespace}${withNetwork ? ` + VM Network ${netName}` : ''} → platform repo`,
	);

	async function stage() {
		if (joining) {
			await api.createNamespace({ name: namespace, project: joinProject });
			return;
		}
		const req: ProjectCreate = { name, namespace };
		const o = splitList(owners);
		if (o.length) req.owners = o;
		if (withNetwork) req.vmNetwork = { name: netName, subnet: subnet.trim() };
		await api.createProject(req);
	}
</script>

<StageModal
	title={adopt ? 'Adopt Tenant' : 'New Project'}
	label={joining ? 'Stage namespace' : 'Stage project'}
	{missing}
	{summary}
	onsubmit={stage}
	{onclose}
>
	{#if adopt && joinable.length > 0}
		<FormField label="Project">
			<SelectInput bind:value={joinProject}>
				<option value="">New project</option>
				{#each joinable as p (p)}<option value={p}>{p}</option>{/each}
			</SelectInput>
		</FormField>
	{/if}
	{#if !joining}
		<FormField label="Project name" error={name && !validName(name) ? NAME_HINT : ''}>
			<TextInput bind:value={name} placeholder="team-c" mono data-autofocus />
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Creates the tenant git repo of the same name.</span
			>
		</FormField>
	{/if}
	<FormField
		label={adopt ? 'Namespace (existing)' : 'First namespace'}
		error={namespace && !validName(namespace) ? NAME_HINT : ''}
	>
		<TextInput
			bind:value={namespace}
			oninput={() => (nsTouched = true)}
			placeholder="team-c"
			mono
			disabled={!!adopt}
		/>
	</FormField>
	{#if !joining}
		<FormField label="Owners (optional)">
			<TextInput bind:value={owners} placeholder="alice bob" />
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Usernames granted admin on the namespace (space/comma separated).</span
			>
		</FormField>
	{/if}

	<!-- Not offered when adopting: the namespace's networking already exists on
	     the cluster; staging a primary UDN on top could break its running VMs. -->
	{#if !adopt}
		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={withNetwork} />
			<span class="text-ink-soft">Add a VM Network — the namespace's primary Segment (Tier-1)</span>
		</label>

		{#if withNetwork}
			<VMNetworkFields base={namespace} bind:name={netName} bind:subnet bind:touched={netTouched} />
		{/if}
	{/if}

	<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
		{#if joining}
			Stages the existing namespace into the platform repo, labeled into
			<span class="font-mono">{joinProject}</span>. On merge, Argo labels
			<span class="font-mono">{adopt}</span> into that project; its VMs then show as untracked, ready
			to adopt into git.
		{:else if adopt}
			Creates the tenant repo now, and stages the existing namespace{#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. On merge, Argo labels
			<span class="font-mono">{adopt}</span> into the project; its VMs then show as untracked, ready to
			adopt into git.
		{:else}
			Creates the tenant repo now, and stages its first namespace{#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. Applied by Argo on merge — open the PR from
			“Changes”.
		{/if}
	</p>
</StageModal>
