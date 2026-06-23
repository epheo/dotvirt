<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api, type EgressIPCreate, type ExternalRouteCreate } from '$lib/api';
	import { TERMS } from '$lib/vocab';

	let {
		namespaces,
		onclose,
		onstaged
	}: {
		namespaces: string[];
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// A Tier-0 (provider-edge) service: a source-NAT pool (EgressIP) pinning a
	// project's egress to fixed, routable IPs, or an external route steering its egress
	// through static next-hop gateways (AdminPolicyBasedExternalRoute). Both are
	// cluster-scoped — proposed to the platform repo.
	let kind = $state<'snat' | 'route'>('snat');
	let name = $state('');
	let ips = $state(''); // egress IPs (snat) or next-hop IPs (route), space/comma separated
	let selectedNs = $state<string[]>([]);
	let submitting = $state(false);
	let error = $state('');

	const list = $derived(
		ips
			.split(/[\s,]+/)
			.map((s) => s.trim())
			.filter(Boolean)
	);
	const valid = $derived(!!name && list.length > 0 && selectedNs.length > 0);

	function toggleNs(ns: string, on: boolean) {
		selectedNs = on ? [...selectedNs, ns] : selectedNs.filter((n) => n !== ns);
	}

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		try {
			if (kind === 'snat') {
				const req: EgressIPCreate = { name, egressIPs: list, namespaces: selectedNs };
				await api.createEgressIP(req);
			} else {
				const req: ExternalRouteCreate = { name, namespaces: selectedNs, nextHops: list };
				await api.createExternalRoute(req);
			}
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
			<h2 class="text-base font-semibold text-slate-800">
				{TERMS.tier0.nsx}
				<span class="font-normal text-slate-400">· {TERMS.tier0.vsphere}</span>
			</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"
				><X size={18} /></button
			>
		</header>
		<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
			<div class="flex gap-2">
				<button
					onclick={() => (kind = 'snat')}
					class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'snat'
						? 'border-blue-500 bg-blue-50 text-blue-700'
						: 'border-slate-300 text-slate-600'}"
				>
					<div class="font-medium">{TERMS.snat.nsx}</div>
					<div class="text-slate-400">Pin egress to fixed IPs (EgressIP)</div>
				</button>
				<button
					onclick={() => (kind = 'route')}
					class="flex-1 rounded border px-3 py-2 text-left text-xs {kind === 'route'
						? 'border-blue-500 bg-blue-50 text-blue-700'
						: 'border-slate-300 text-slate-600'}"
				>
					<div class="font-medium">External Route</div>
					<div class="text-slate-400">Steer egress via next-hops</div>
				</button>
			</div>

			<label class="block">
				<span class="text-slate-600">Name</span>
				<input
					bind:value={name}
					placeholder={kind === 'snat' ? 'team-a-snat' : 'team-a-gw'}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>

			<label class="block">
				<span class="text-slate-600"
					>{kind === 'snat' ? 'Egress IPs' : 'Next-hop IPs'}
					<span class="text-slate-400">(space/comma separated)</span></span
				>
				<input
					bind:value={ips}
					placeholder={kind === 'snat' ? '192.0.2.10 192.0.2.11' : '10.0.0.1'}
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
			</label>

			<div>
				<span class="text-slate-600">Applies to projects</span>
				<div class="mt-1 max-h-28 space-y-1 overflow-y-auto rounded border border-slate-300 p-2">
					{#each namespaces as ns (ns)}
						<label class="flex items-center gap-2 text-xs">
							<input
								type="checkbox"
								checked={selectedNs.includes(ns)}
								onchange={(e) => toggleNs(ns, e.currentTarget.checked)}
							/>
							<span class="text-slate-700">{ns}</span>
						</label>
					{/each}
				</div>
			</div>

			<p class="rounded bg-slate-50 px-3 py-2 text-xs text-slate-500">
				{#if kind === 'snat'}
					A {TERMS.snat.nsx} pool ({TERMS.snat.backing}) pins the selected projects' north-south
					egress to these fixed, routable source IPs.
				{:else}
					An external route (AdminPolicyBasedExternalRoute) steers the selected projects' egress
					through these static next-hop gateways.
				{/if}
				Cluster-scoped — proposed to the platform repository.
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
				{submitting ? 'Staging…' : 'Stage service'}
			</button>
		</footer>
	</div>
</div>
