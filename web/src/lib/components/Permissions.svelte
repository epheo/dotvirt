<script lang="ts">
	// The Permissions tab (vCenter quartet: Summary · Monitor · Configure ·
	// Permissions): the caller's effective capabilities per namespace, from a
	// SelfSubjectRulesReview under their own token. Read-only — dotvirt grants
	// nothing; RBAC is managed by the platform.
	import { untrack } from 'svelte';
	import { Check, X } from 'lucide-svelte';
	import { api, type Permissions } from '$lib/api';

	let { namespaces }: { namespaces: string[] } = $props();

	let data = $state<Permissions[] | null>(null);
	let error = $state('');

	// Key on the namespace SET so per-frame array identities don't refetch.
	const key = $derived([...namespaces].sort().join(' '));
	$effect(() => {
		key;
		data = null;
		error = '';
		Promise.all(untrack(() => [...namespaces]).map((ns) => api.permissions(ns)))
			.then((d) => (data = d))
			.catch((e) => (error = String(e)));
	});
</script>

{#if error}
	<div class="rounded border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
		Couldn't read permissions: {error}
	</div>
{:else if !data}
	<div class="py-8 text-center text-sm text-slate-400">Checking your access…</div>
{:else}
	<div class="space-y-4">
		{#each data as p (p.namespace)}
			<section class="max-w-2xl rounded border border-slate-200">
				<h3
					class="border-b border-slate-200 bg-slate-50 px-3 py-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase"
				>
					Your access in {p.namespace}
				</h3>
				<ul class="divide-y divide-slate-100 text-[13px]">
					{#each p.capabilities as c (c.id)}
						<li class="flex items-center gap-2 px-3 py-1.5" title={c.detail}>
							{#if c.allowed}
								<Check size={14} class="shrink-0 text-green-600" />
							{:else}
								<X size={14} class="shrink-0 text-slate-300" />
							{/if}
							<span class={c.allowed ? 'text-slate-800' : 'text-slate-400'}>{c.label}</span>
						</li>
					{/each}
				</ul>
				{#if p.incomplete}
					<p class="border-t border-amber-100 bg-amber-50 px-3 py-1.5 text-xs text-amber-700">
						The cluster couldn't enumerate every rule; some allowed actions may show as denied.
					</p>
				{/if}
			</section>
		{/each}
		<p class="max-w-2xl text-xs text-slate-400">
			These reflect your Kubernetes RBAC, evaluated with your own token. Configuration, power, and
			delete aren't listed: they go through a pull request, where the project's repository decides
			who merges. Access itself is granted by the platform, not dotvirt.
		</p>
	</div>
{/if}
