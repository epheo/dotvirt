<script lang="ts">
	import { ChevronDown, ChevronRight, Folder, X } from 'lucide-svelte';
	import { api, type DraftView, type Proposal, type ProposeResult } from '$lib/api';
	import ChangeList from './ChangeList.svelte';

	let {
		drafts,
		proposals,
		onclose,
		onchanged
	}: {
		drafts: { project: string; draft: DraftView }[];
		proposals: Proposal[];
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
		<button onclick={onclose} class="text-slate-400 hover:text-slate-700"><X size={18} /></button>
	</header>

	<div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
		<!-- Open PRs (persistent, fetched from Forgejo) — survive closing the panel,
		     unlike the transient propose response below. -->
		{#each proposals as p (p.project)}
			<div
				class="mb-2 flex items-center gap-2 rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm"
			>
				<span class="font-medium text-slate-700">{p.project}</span>
				<span class="rounded bg-emerald-100 px-1.5 text-xs font-medium text-emerald-700">
					PR #{p.prNumber} open
				</span>
				<a
					href={p.prURL}
					target="_blank"
					rel="noopener"
					class="ml-auto min-w-0 truncate text-xs text-blue-700 underline">{p.title || p.prURL}</a
				>
			</div>
		{/each}

		<!-- Transient propose feedback. Open PRs are shown persistently above (from
		     `proposals`), so here we surface only the no-PR cases: forge unconfigured
		     (compare link) or push-only. -->
		{#each Object.entries(result).filter(([, r]) => !r.prURL) as [project, r] (project)}
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
					<Folder size={14} class="text-blue-500" />
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
									class="mt-2 flex items-center gap-1 text-xs text-slate-400 hover:text-slate-600"
								>
									{#if showYaml[k]}<ChevronDown size={12} /> hide YAML{:else}<ChevronRight size={12} /> view YAML{/if}
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
