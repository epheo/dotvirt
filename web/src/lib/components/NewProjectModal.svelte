<script lang="ts">
	import { api, type ProjectCreate } from '$lib/api';
	import { validName, NAME_HINT, validCIDR, CIDR_HINT } from '$lib/validate';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';

	let {
		onclose,
		onstaged,
	}: {
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let name = $state(''); // project name → tenant repo + dotvirt.io/project
	let namespace = $state(''); // first namespace
	let owners = $state(''); // space/comma-separated usernames
	let withNetwork = $state(true);
	let netName = $state('');
	let subnet = $state('');

	let submitting = $state(false);
	let error = $state('');

	// The first namespace defaults to the project name until the user overrides it.
	let nsTouched = $state(false);
	$effect(() => {
		if (!nsTouched) namespace = name;
	});
	// The VM Network name defaults to "<namespace>-net" until overridden.
	let netTouched = $state(false);
	$effect(() => {
		if (!netTouched) netName = namespace ? `${namespace}-net` : '';
	});

	const missing = $derived.by(() => {
		const m: string[] = [];
		if (!name) m.push('Project name is required');
		else if (!validName(name)) m.push('Project name must be lowercase alphanumeric with dashes');
		if (!namespace) m.push('First namespace is required');
		else if (!validName(namespace)) m.push('Namespace must be lowercase alphanumeric with dashes');
		if (withNetwork) {
			if (!netName) m.push('VM Network name is required');
			if (!subnet.trim()) m.push('Subnet is required for a primary network');
			else if (!validCIDR(subnet.trim())) m.push('Subnet must be a CIDR (e.g. 10.40.0.0/16)');
		}
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Creates repo “${name}”; stages namespace ${namespace}${withNetwork ? ` + VM Network ${netName}` : ''} → platform repo`
			: '',
	);

	const parseOwners = (s: string): string[] =>
		s
			.split(/[\s,]+/)
			.map((o) => o.trim())
			.filter(Boolean);

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		const req: ProjectCreate = { name, namespace };
		const o = parseOwners(owners);
		if (o.length) req.owners = o;
		if (withNetwork) req.vmNetwork = { name: netName, subnet: subnet.trim() };
		try {
			await api.createProject(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="New Project" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<FormField label="Project name" error={name && !validName(name) ? NAME_HINT : ''}>
			<TextInput bind:value={name} placeholder="team-c" mono data-autofocus />
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Creates the tenant git repo of the same name.</span
			>
		</FormField>
		<FormField label="First namespace" error={namespace && !validName(namespace) ? NAME_HINT : ''}>
			<TextInput
				bind:value={namespace}
				oninput={() => (nsTouched = true)}
				placeholder="team-c"
				mono
			/>
		</FormField>
		<FormField label="Owners (optional)">
			<TextInput bind:value={owners} placeholder="alice bob" />
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Usernames granted admin on the namespace (space/comma separated).</span
			>
		</FormField>

		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={withNetwork} />
			<span class="text-ink-soft">Add a VM Network — the namespace's primary Segment (Tier-1)</span>
		</label>

		{#if withNetwork}
			<div class="space-y-3 rounded border border-line p-3">
				<FormField label="VM Network name">
					<TextInput bind:value={netName} oninput={() => (netTouched = true)} mono />
				</FormField>
				<FormField
					label="Subnet (CIDR — required for a primary network)"
					error={subnet && !validCIDR(subnet.trim()) ? CIDR_HINT : ''}
				>
					<TextInput bind:value={subnet} placeholder="10.40.0.0/16" mono />
				</FormField>
				<p class="text-[11px] text-ink-faint">
					A flat layer-2 network that follows VMs across nodes (keeps their IP on migration).
				</p>
			</div>
		{/if}

		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			Creates the tenant repo now, and stages its first namespace{#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. Applied by Argo on merge — open the PR from
			“Changes”.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage project"
			disabled={!valid}
			{missing}
			{summary}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
