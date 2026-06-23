<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type NamespaceCreate } from '$lib/api';

	let {
		projects = [],
		project: initialProject,
		onclose,
		onstaged
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

<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
	onclick={(e) => e.target === e.currentTarget && onclose()}
	onkeydown={(e) => e.key === 'Escape' && onclose()}
	role="presentation"
>
	<div class="flex max-h-[90vh] w-full max-w-md flex-col rounded-lg bg-white shadow-xl">
		<header class="flex items-center justify-between border-b border-slate-200 px-5 py-3">
			<h2 class="text-base font-semibold text-slate-800">New Namespace</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"
				><X size={18} /></button
			>
		</header>
		<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
			<label class="block">
				<span class="text-slate-600">Name</span>
				<input
					bind:value={name}
					placeholder="tenant-c"
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>
			<label class="block">
				<span class="text-slate-600">Project</span>
				<select bind:value={project} class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5">
					{#each projects as p (p)}<option value={p}>{p}</option>{/each}
				</select>
			</label>

			<label class="flex items-center gap-2">
				<input type="checkbox" bind:checked={withNetwork} />
				<span class="text-slate-600">Add a VM Network — the namespace's primary Segment (Tier-1)</span>
			</label>

			{#if withNetwork}
				<div class="space-y-3 rounded border border-slate-200 p-3">
					<label class="block">
						<span class="text-slate-600">VM Network name</span>
						<input
							bind:value={netName}
							oninput={() => (netTouched = true)}
							class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
						/>
					</label>
					<label class="block">
						<span class="text-slate-600"
							>Subnet <span class="text-slate-400">(CIDR — required for a primary network)</span></span
						>
						<input
							bind:value={subnet}
							placeholder="10.40.0.0/16"
							class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
						/>
					</label>
					<p class="text-[11px] text-slate-400">
						A flat layer-2 network that follows VMs across nodes (keeps their IP on migration).
					</p>
				</div>
			{/if}

			<p class="rounded bg-slate-50 px-3 py-2 text-xs text-slate-500">
				Creates a namespace in this project (labeled so dotvirt adopts it){#if withNetwork}, with a
					primary "VM Network" its VMs attach to by default{/if}. Applied by the project's Argo app on
				merge.
			</p>
			{#if error}
				<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
			{/if}
		</div>
		<footer class="flex items-center gap-2 border-t border-slate-200 px-5 py-3">
			<span class="text-xs text-slate-400">Staged into the changeset; open a PR from “Changes”.</span>
			<button
				onclick={onclose}
				class="ml-auto rounded px-4 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Cancel</button
			>
			<button
				onclick={submit}
				disabled={!valid || submitting}
				class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
			>
				{submitting ? 'Staging…' : 'Stage namespace'}
			</button>
		</footer>
	</div>
</div>
