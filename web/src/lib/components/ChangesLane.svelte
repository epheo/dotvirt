<script lang="ts">
	import { ChevronDown, ChevronRight, Folder } from 'lucide-svelte';
	import { api, type DraftView, type ProposeResult } from '$lib/api';
	import ChangeList from './ChangeList.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';

	// One project's staged-changes lane: the items, their diffs, and the propose
	// form. All form state is lane-local, so a lane that disappears (proposed or
	// discarded) takes its state with it. The propose result outlives the lane —
	// it's handed up to the panel, which renders it until the PR banner lands.
	let {
		project,
		draft,
		onchanged,
		onproposed,
	}: {
		project: string;
		draft: DraftView;
		onchanged: () => void;
		// The typed title rides along so the panel's synthesized PR banner reads
		// exactly like the live one that later takes over.
		onproposed: (r: ProposeResult, title: string) => void;
	} = $props();

	let title = $state('');
	let message = $state('');
	let error = $state('');
	let proposing = $state(false);
	let discarding = $state(false);
	let unstaging = $state<string | null>(null); // item key, while its unstage is in flight
	let showYaml = $state<Record<string, boolean>>({});

	// A successful propose consumed this draft server-side; hide the lane at once
	// instead of showing consumed items under the new PR banner until the summary
	// round-trips. The next summary clears the flag — if the draft genuinely still
	// has items (partial failure, or new staging), they come back.
	let proposed = $state(false);
	$effect(() => {
		draft;
		proposed = false;
	});

	const itemKey = (ns: string, name: string) => `${ns}/${name}`;

	async function unstage(ns: string, name: string, resource?: string) {
		const k = itemKey(ns, name);
		if (unstaging) return;
		unstaging = k;
		error = '';
		try {
			await api.unstage(ns, name, resource, project);
			onchanged();
		} catch (e) {
			error = String(e);
		} finally {
			unstaging = null;
		}
	}

	async function discardAll() {
		if (discarding) return;
		discarding = true;
		error = '';
		try {
			await api.discardDraft(project);
			onchanged();
		} catch (e) {
			error = String(e);
		} finally {
			discarding = false;
		}
	}

	async function propose() {
		if (proposing) return;
		proposing = true;
		error = '';
		try {
			const r = await api.propose(project, title, message);
			onproposed(r, title);
			title = '';
			message = '';
			proposed = true;
			onchanged();
		} catch (e) {
			// The push may have landed before the error (e.g. a gateway timeout on
			// the PR step) — re-read the summary so the lane reflects server truth.
			error = String(e);
			onchanged();
		} finally {
			proposing = false;
		}
	}
</script>

<section class="mb-5" hidden={proposed}>
	<div class="mb-1 flex items-center gap-2">
		<Folder size={14} class="text-blue-500" />
		<span class="font-semibold text-ink-soft">{project}</span>
		<span class="text-xs text-ink-faint">({draft.count})</span>
		<button
			onclick={discardAll}
			disabled={discarding}
			class="ml-auto text-xs text-ink-muted hover:text-ink-soft disabled:text-ink-faint"
			>{discarding ? 'discarding…' : 'discard all'}</button
		>
	</div>
	<div class="mb-2">
		<GitOpsStepper stage="staged" />
	</div>

	{#if error}
		<pre class="mb-2 rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
	{/if}

	{#each draft.items as item (itemKey(item.namespace, item.name))}
		{@const k = itemKey(item.namespace, item.name)}
		<div class="mb-2 rounded border border-slate-200">
			<div class="flex items-center gap-2 border-b border-slate-100 px-3 py-2">
				<span
					class="rounded px-1.5 py-0.5 text-xs {item.kind === 'delete'
						? 'bg-red-100 text-red-700'
						: item.kind === 'create'
							? 'bg-green-100 text-green-700'
							: 'bg-blue-100 text-blue-700'}">{item.kind}</span
				>
				<span class="font-medium text-ink">{item.namespace}/{item.name}</span>
				<button
					onclick={() => unstage(item.namespace, item.name, item.resource)}
					disabled={unstaging !== null}
					class="ml-auto text-xs text-red-500 hover:text-red-700 disabled:text-ink-faint"
					>{unstaging === k ? 'unstaging…' : 'unstage'}</button
				>
			</div>
			<div class="px-3 py-2">
				<ChangeList changes={item.changes} />
				{#if item.yaml}
					<button
						onclick={() => (showYaml[k] = !showYaml[k])}
						class="mt-2 flex items-center gap-1 text-xs text-ink-faint hover:text-ink-soft"
					>
						{#if showYaml[k]}<ChevronDown size={12} /> hide YAML{:else}<ChevronRight size={12} /> view
							YAML{/if}
					</button>
					{#if showYaml[k]}
						<pre
							class="mt-1 overflow-x-auto rounded bg-slate-50 p-2 font-mono text-[11px] leading-snug text-ink-soft">{item.yaml}</pre>
					{/if}
				{/if}
			</div>
		</div>
	{/each}

	<div class="mt-2 space-y-2">
		<input
			bind:value={title}
			placeholder="Pull request title"
			class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"
		/>
		<textarea
			bind:value={message}
			placeholder="Description (optional)"
			rows="2"
			class="w-full rounded border border-slate-300 px-2 py-1.5 text-sm"></textarea>
		<button
			onclick={propose}
			disabled={proposing}
			class="w-full rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
		>
			{proposing ? 'Proposing…' : `Propose pull request → ${project}`}
		</button>
	</div>
</section>
