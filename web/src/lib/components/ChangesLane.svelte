<script lang="ts">
	import { ChevronDown, ChevronRight, Folder, TriangleAlert } from 'lucide-svelte';
	import { api, type DraftView, type ProposeResult } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import { draftKindTone, TONE_PILL } from '$lib/status';
	import ChangeList from './ChangeList.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import GitOpsStepper from './GitOpsStepper.svelte';
	import Note from './Note.svelte';
	import TextInput from './TextInput.svelte';

	// One project's staged-changes lane: the items, their diffs, and the propose
	// form. All form state is lane-local, so a lane that disappears (proposed or
	// discarded) takes its state with it. The propose result outlives the lane -
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
	// Three actions because three busy labels show independently (Propose button,
	// discard-all link, per-item unstage); they share one ErrorNote below.
	const proposeOp = action();
	const discardOp = action();
	const unstageOp = action();
	const error = $derived(proposeOp.error || discardOp.error || unstageOp.error);
	const clearErrors = () => (proposeOp.clear(), discardOp.clear(), unstageOp.clear());
	let unstaging = $state<string | null>(null); // item key, while its unstage is in flight
	let showYaml = $state<Record<string, boolean>>({});

	// A successful propose consumed this draft server-side; hide the lane at once
	// instead of showing consumed items under the new PR banner until the summary
	// round-trips. The next summary clears the flag - if the draft genuinely still
	// has items (partial failure, or new staging), they come back.
	let proposed = $state(false);
	$effect(() => {
		draft;
		proposed = false;
	});

	// Mirrors the draft store's own key (empty resource == vm): whole-namespace adoption
	// can stage a VM and a policy of the same ns/name, and a key without the resource
	// would both collide the unstage spinner and throw each_key_duplicate.
	const itemKey = (ns: string, name: string, resource?: string) =>
		`${resource || 'vm'}:${ns}/${name}`;

	async function unstage(ns: string, name: string, resource?: string) {
		const k = itemKey(ns, name, resource);
		if (unstaging) return;
		unstaging = k;
		clearErrors();
		if (await unstageOp.run(() => api.unstage(ns, name, resource, project))) onchanged();
		unstaging = null;
	}

	async function discardAll() {
		if (discardOp.busy) return;
		clearErrors();
		if (await discardOp.run(() => api.discardDraft(project))) onchanged();
	}

	async function propose() {
		if (proposeOp.busy) return;
		clearErrors();
		await proposeOp.run(async () => {
			const r = await api.propose(project, title, message);
			onproposed(r, title);
			title = '';
			message = '';
			proposed = true;
		});
		// On success AND on failure: the push may have landed before the error
		// (e.g. a gateway timeout on the PR step) - re-read the summary so the
		// lane reflects server truth.
		onchanged();
	}
</script>

<section class="mb-5" hidden={proposed}>
	<div class="mb-1 flex items-center gap-2">
		<Folder size={14} class="text-accent" />
		<span class="font-semibold text-ink-soft">{project}</span>
		<span class="text-xs text-ink-faint">({draft.count})</span>
		{#if draft.count > 0}
			<button
				onclick={discardAll}
				disabled={discardOp.busy}
				class="ml-auto text-xs text-ink-muted hover:text-ink-soft disabled:text-ink-faint"
				>{discardOp.busy ? 'discarding…' : 'discard all'}</button
			>
		{/if}
	</div>
	{#if draft.count > 0}
		<div class="mb-2">
			<GitOpsStepper stage="staged" />
		</div>
	{/if}

	<ErrorNote {error} class="mb-2" />

	<!-- Recomputed every fetch: what ArgoCD would prune that this draft does not
	     speak for. Shows on empty drafts so recovery warns BEFORE the first merge. -->
	{#if draft.warning}
		<Note tone="warn" class="mb-2 flex items-start gap-2">
			<TriangleAlert size={14} class="mt-0.5 shrink-0" />
			<span>{draft.warning}</span>
		</Note>
	{/if}

	{#each draft.items as item (itemKey(item.namespace, item.name, item.resource))}
		{@const k = itemKey(item.namespace, item.name, item.resource)}
		<div class="mb-2 rounded border border-line">
			<div class="flex items-center gap-2 border-b border-line-soft px-3 py-2">
				<span class="rounded px-1.5 py-0.5 text-xs {TONE_PILL[draftKindTone(item.kind)]}"
					>{item.kind}</span
				>
				<span class="font-medium text-ink">{item.namespace}/{item.name}</span>
				<button
					onclick={() => unstage(item.namespace, item.name, item.resource)}
					disabled={unstaging !== null}
					class="ml-auto text-xs text-danger hover:text-danger-ink disabled:text-ink-faint"
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
							class="mt-1 overflow-x-auto rounded bg-inset p-2 font-mono text-[11px] leading-snug text-ink-soft">{item.yaml}</pre>
					{/if}
				{/if}
			</div>
		</div>
	{/each}

	{#if draft.count > 0}
		<div class="mt-2 space-y-2">
			<TextInput bind:value={title} placeholder="Pull request title" />
			<textarea
				bind:value={message}
				placeholder="Description (optional)"
				rows="2"
				class="w-full rounded border border-line-strong px-2 py-1.5 text-sm"></textarea>
			<button
				onclick={propose}
				disabled={proposeOp.busy}
				class="w-full rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:bg-line-strong"
			>
				{proposeOp.busy ? 'Proposing…' : `Propose pull request -> ${project}`}
			</button>
		</div>
	{/if}
</section>
