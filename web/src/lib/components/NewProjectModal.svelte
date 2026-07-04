<script lang="ts">
	import { api, type ProjectCreate } from '$lib/api';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

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

	const valid = $derived(!!(name && namespace) && (!withNetwork || !!(netName && subnet.trim())));

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
		<label class="block">
			<span class="text-ink-soft">Project name</span>
			<input
				bind:value={name}
				placeholder="team-c"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Creates the tenant git repo of the same name.</span
			>
		</label>
		<label class="block">
			<span class="text-ink-soft">First namespace</span>
			<input
				bind:value={namespace}
				oninput={() => (nsTouched = true)}
				placeholder="team-c"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>
		<label class="block">
			<span class="text-ink-soft">Owners <span class="text-ink-faint">(optional)</span></span>
			<input
				bind:value={owners}
				placeholder="alice bob"
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Usernames granted admin on the namespace (space/comma separated).</span
			>
		</label>

		<label class="flex items-center gap-2">
			<input type="checkbox" bind:checked={withNetwork} />
			<span class="text-ink-soft">Add a VM Network — the namespace's primary Segment (Tier-1)</span
			>
		</label>

		{#if withNetwork}
			<div class="space-y-3 rounded border border-slate-200 p-3">
				<label class="block">
					<span class="text-ink-soft">VM Network name</span>
					<input
						bind:value={netName}
						oninput={() => (netTouched = true)}
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					/>
				</label>
				<label class="block">
					<span class="text-ink-soft"
						>Subnet <span class="text-ink-faint">(CIDR — required for a primary network)</span
						></span
					>
					<input
						bind:value={subnet}
						placeholder="10.40.0.0/16"
						class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
					/>
				</label>
				<p class="text-[11px] text-ink-faint">
					A flat layer-2 network that follows VMs across nodes (keeps their IP on migration).
				</p>
			</div>
		{/if}

		<p class="rounded bg-slate-50 px-3 py-2 text-xs text-ink-muted">
			Creates the tenant repo now, and stages its first namespace{#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. Applied by Argo on merge — open the PR from
			“Changes”.
		</p>
		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<StageFooter
			label="Stage project"
			disabled={!valid}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
