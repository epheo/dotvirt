<script lang="ts">
	import { api, type DraftView, type ProposeResult } from '$lib/api';
	import ChangeList from './ChangeList.svelte';

	let {
		drafts,
		onclose,
		onchanged
	}: {
		drafts: { project: string; draft: DraftView }[];
		onclose: () => void;
		onchanged: () => void;
	} = $props();

	// Per-project PR form state, keyed by project name.
	let title = $state<Record<string, string>>({});
	let message = $state<Record<string, string>>({});
	let busy = $state<Record<string, boolean>>({});
	let error = $state<Record<string, string>>({});
	let result = $state<Record<string, ProposeResult>>({});
	let showYaml = $state<Record<string, boolean>>({});

	const total = $derived(drafts.reduce((n, d) => n + d.draft.count, 0));
	const itemKey = (p: string, ns: string, name: string) => `${p}|${ns}/${name}`;

	async function unstage(project: string, ns: string, name: string) {
		await api.unstage(ns, name);
		onchanged();
	}

	async function discardAll(project: string) {
		await api.discardDraft(project);
		onchanged();
	}

	async function propose(project: string) {
		busy[project] = true;
		error[project] = '';
		try {
			result[project] = await api.propose(project, title[project] ?? '', message[project] ?? '');
			onchanged();
		} catch (e) {
			error[project] = String(e);
		} finally {
			busy[project] = false;
		}
	}
</script>

<aside class="flex h-full w-[28rem] flex-col border-l border-slate-300 bg-white shadow-xl">
	<header class="flex items-center justify-between border-b border-slate-200 px-4 py-3">
		<h2 class="text-base font-semibold text-slate-800">
			Changes <span class="text-slate-400">({total})</span>
		</h2>
		<button onclick={onclose} class="text-slate-400 hover:text-slate-700">✕</button>
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		<!-- Proposal results live OUTSIDE the per-project loop: proposing empties a
		     draft, so its project drops out of `drafts` and its section unmounts — the
		     PR link must persist here regardless. -->
		{#each Object.entries(result) as [project, r] (project)}
			<div class="mb-2 rounded border border-green-200 bg-green-50 p-3 text-sm">
				<div class="mb-1 flex items-center gap-1">
					<span class="font-medium text-slate-700">{project}</span>
					<button
						onclick={() => delete result[project]}
						class="ml-auto text-xs text-slate-400 hover:text-slate-600">dismiss</button
					>
				</div>
				{#if r.prURL}
					<p class="text-slate-700">Pull request opened{r.existing ? ' (existing)' : ''}:</p>
					<a href={r.prURL} target="_blank" rel="noopener" class="font-medium text-blue-700 underline"
						>{r.prURL}</a
					>
				{:else if r.compareURL}
					<p class="text-slate-700">Branch <code>{r.branch}</code> pushed. Open a PR:</p>
					<a
						href={r.compareURL}
						target="_blank"
						rel="noopener"
						class="font-medium text-blue-700 underline">{r.compareURL}</a
					>
				{:else}
					<p class="text-slate-700">
						Branch <code>{r.branch}</code> pushed{r.pushed ? '' : ' (local only)'}.
					</p>
				{/if}
			</div>
		{/each}

		{#if total === 0}
			<p class="py-8 text-center text-sm text-slate-400">
				No pending changes. Edit a VM or create one to stage changes here.
			</p>
		{/if}

		{#each drafts as { project, draft } (project)}
			<section class="mb-5">
				<div class="mb-2 flex items-center gap-2">
					<span class="text-blue-500">▦</span>
					<span class="font-semibold text-slate-700">{project}</span>
					<span class="text-xs text-slate-400">({draft.count})</span>
					<button
						onclick={() => discardAll(project)}
						class="ml-auto text-xs text-slate-500 hover:text-slate-700">discard all</button
					>
				</div>

				{#if error[project]}
					<pre
						class="mb-2 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error[
							project
						]}</pre>
				{/if}

				{#each draft.items as item (itemKey(project, item.namespace, item.name))}
					{@const k = itemKey(project, item.namespace, item.name)}
					<div class="mb-2 rounded border border-slate-200">
						<div class="flex items-center gap-2 border-b border-slate-100 px-3 py-2">
							<span
								class="rounded px-1.5 py-0.5 text-xs {item.kind === 'delete'
									? 'bg-red-100 text-red-700'
									: item.kind === 'create'
										? 'bg-green-100 text-green-700'
										: 'bg-blue-100 text-blue-700'}">{item.kind}</span
							>
							<span class="font-medium text-slate-800">{item.namespace}/{item.name}</span>
							<button
								onclick={() => unstage(project, item.namespace, item.name)}
								class="ml-auto text-xs text-red-500 hover:text-red-700">unstage</button
							>
						</div>
						<div class="px-3 py-2">
							<ChangeList changes={item.changes} />
							{#if item.yaml}
								<button
									onclick={() => (showYaml[k] = !showYaml[k])}
									class="mt-2 text-xs text-slate-400 hover:text-slate-600"
								>
									{showYaml[k] ? '▾ hide YAML' : '▸ view YAML'}
								</button>
								{#if showYaml[k]}
									<pre
										class="mt-1 overflow-x-auto rounded bg-slate-50 p-2 font-mono text-[11px] leading-snug text-slate-600">{item.yaml}</pre>
								{/if}
							{/if}
						</div>
					</div>
				{/each}

				<div class="mt-2 space-y-2">
					<input
						bind:value={title[project]}
						placeholder="Pull request title"
						class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"
					/>
					<textarea
						bind:value={message[project]}
						placeholder="Description (optional)"
						rows="2"
						class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"
					></textarea>
					<button
						onclick={() => propose(project)}
						disabled={busy[project]}
						class="w-full rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
					>
						{busy[project] ? 'Proposing…' : `Create pull request → ${project}`}
					</button>
				</div>
			</section>
		{/each}
	</div>
</aside>
