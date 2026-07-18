<script lang="ts">
	import { api, type NamespaceCreate } from '$lib/api';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import FormField from './FormField.svelte';

	let {
		projects = [],
		project: initialProject,
		onclose,
		onstaged,
	}: {
		projects?: string[]; // repo-backed projects the namespace can join
		project?: string; // preselected project (e.g. from a project's context menu)
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let name = $state('');
	let project = $state('');
	let withNetwork = $state(true);
	let netName = $state('');
	let subnet = $state('');

	let submitting = $state(false);
	let error = $state('');

	$effect(() => {
		if (!project) project = initialProject ?? projects[0] ?? '';
	});
	// Default the VM Network name to "<namespace>-net" until the user overrides it.
	let netTouched = $state(false);
	$effect(() => {
		if (!netTouched) netName = name ? `${name}-net` : '';
	});

	const valid = $derived(!!(name && project) && (!withNetwork || !!(netName && subnet.trim())));

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		const req: NamespaceCreate = { name, project };
		if (withNetwork) {
			// A VM Network is a primary UDN, which must do IPAM — the subnet is required.
			req.vmNetwork = { name: netName, subnet: subnet.trim() };
		}
		try {
			await api.createNamespace(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="New Namespace" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<FormField label="Name">
			<input
				bind:value={name}
				placeholder="tenant-c"
				class="w-full rounded border border-line-strong px-2 py-1.5"
			/>
		</FormField>
		<FormField label="Project">
			<select bind:value={project} class="w-full rounded border border-line-strong px-2 py-1.5">
				{#each projects as p (p)}<option value={p}>{p}</option>{/each}
			</select>
		</FormField>

		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={withNetwork} />
			<span class="text-ink-soft">Add a VM Network — the namespace's primary Segment (Tier-1)</span>
		</label>

		{#if withNetwork}
			<div class="space-y-3 rounded border border-line p-3">
				<FormField label="VM Network name">
					<input
						bind:value={netName}
						oninput={() => (netTouched = true)}
						class="w-full rounded border border-line-strong px-2 py-1.5"
					/>
				</FormField>
				<label class="block">
					<span class="text-ink-soft"
						>Subnet <span class="text-ink-faint">(CIDR — required for a primary network)</span
						></span
					>
					<input
						bind:value={subnet}
						placeholder="10.40.0.0/16"
						class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
					/>
				</label>
				<p class="text-[11px] text-ink-faint">
					A flat layer-2 network that follows VMs across nodes (keeps their IP on migration).
				</p>
			</div>
		{/if}

		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			Creates a namespace in this project (labeled so dotvirt adopts it){#if withNetwork}, with a
				primary "VM Network" its VMs attach to by default{/if}. Applied by the project's Argo app on
			merge.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage namespace"
			disabled={!valid}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
