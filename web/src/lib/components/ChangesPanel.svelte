<script lang="ts">
	import { api, type DraftView, type ProposeResult } from '$lib/api';
	import ChangeList from './ChangeList.svelte';

	let { onclose, onchanged }: { onclose: () => void; onchanged: () => void } = $props();

	let draft = $state<DraftView | null>(null);
	let title = $state('');
	let message = $state('');
	let busy = $state(false);
	let error = $state('');
	let result = $state<ProposeResult | null>(null);
	let showYaml = $state<Record<string, boolean>>({});

	async function load() {
		try {
			draft = await api.getDraft();
		} catch (e) {
			error = String(e);
		}
	}

	$effect(() => {
		load();
	});

	async function unstage(ns: string, name: string) {
		await api.unstage(ns, name);
		await load();
		onchanged();
	}

	async function discardAll() {
		await api.discardDraft();
		await load();
		onchanged();
	}

	async function propose() {
		busy = true;
		error = '';
		try {
			result = await api.propose(title, message);
			await load(); // draft is now empty
			onchanged();
		} catch (e) {
			error = String(e);
		} finally {
			busy = false;
		}
	}

	const key = (ns: string, name: string) => `${ns}/${name}`;
</script>

<aside class="flex h-full w-[28rem] flex-col border-l border-slate-300 bg-white shadow-xl">
	<header class="flex items-center justify-between border-b border-slate-200 px-4 py-3">
		<h2 class="text-base font-semibold text-slate-800">
			Changes {#if draft}<span class="text-slate-400">({draft.count})</span>{/if}
		</h2>
		<button onclick={onclose} class="text-slate-400 hover:text-slate-700">✕</button>
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		{#if result}
			<div class="rounded border border-green-200 bg-green-50 p-3 text-sm">
				{#if result.prURL}
					<p class="text-slate-700">Pull request opened{result.existing ? ' (existing)' : ''}:</p>
					<a href={result.prURL} target="_blank" rel="noopener" class="font-medium text-blue-700 underline">
						{result.prURL}
					</a>
				{:else if result.compareURL}
					<p class="text-slate-700">Branch <code>{result.branch}</code> pushed. Open a PR:</p>
					<a href={result.compareURL} target="_blank" rel="noopener" class="font-medium text-blue-700 underline">{result.compareURL}</a>
				{:else}
					<p class="text-slate-700">Branch <code>{result.branch}</code> pushed{result.pushed ? '' : ' (local only)'}.</p>
				{/if}
			</div>
		{/if}

		{#if error}
			<pre class="mb-3 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}

		{#if draft && draft.count === 0 && !result}
			<p class="py-8 text-center text-sm text-slate-400">No pending changes. Edit a VM or create one to stage changes here.</p>
		{:else if draft}
			{#each draft.items as item (key(item.namespace, item.name))}
				<div class="mb-3 rounded border border-slate-200">
					<div class="flex items-center gap-2 border-b border-slate-100 px-3 py-2">
						<span class="rounded px-1.5 py-0.5 text-xs {item.kind === 'create' ? 'bg-green-100 text-green-700' : 'bg-blue-100 text-blue-700'}">
							{item.kind}
						</span>
						<span class="font-medium text-slate-800">{item.namespace}/{item.name}</span>
						<button onclick={() => unstage(item.namespace, item.name)} class="ml-auto text-xs text-red-500 hover:text-red-700">unstage</button>
					</div>
					<div class="px-3 py-2">
						<ChangeList changes={item.changes} />
						{#if item.yaml}
							<button
								onclick={() => (showYaml[key(item.namespace, item.name)] = !showYaml[key(item.namespace, item.name)])}
								class="mt-2 text-xs text-slate-400 hover:text-slate-600"
							>
								{showYaml[key(item.namespace, item.name)] ? '▾ hide YAML' : '▸ view YAML'}
							</button>
							{#if showYaml[key(item.namespace, item.name)]}
								<pre class="mt-1 overflow-x-auto rounded bg-slate-50 p-2 font-mono text-[11px] leading-snug text-slate-600">{item.yaml}</pre>
							{/if}
						{/if}
					</div>
				</div>
			{/each}
		{/if}
	</div>

	{#if draft && draft.count > 0}
		<footer class="space-y-2 border-t border-slate-200 px-4 py-3">
			<input bind:value={title} placeholder="Pull request title" class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm" />
			<textarea bind:value={message} placeholder="Description (optional)" rows="2" class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"></textarea>
			<div class="flex gap-2">
				<button onclick={discardAll} class="rounded px-3 py-1.5 text-sm text-slate-600 hover:bg-slate-100">Discard all</button>
				<button onclick={propose} disabled={busy} class="ml-auto rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300">
					{busy ? 'Proposing…' : 'Create pull request'}
				</button>
			</div>
		</footer>
	{/if}
</aside>
