<script lang="ts">
	import { api, type Commit } from '#lib/api.js';
	import { relativeAge } from '#lib/format.js';
	import { changesHref } from '#lib/nav.js';
	import { resource } from '#lib/resource.svelte.js';
	import { approvalLine } from '#lib/review.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { TONE_TEXT } from '#lib/status.js';
	import {
		Check,
		ExternalLink,
		GitPullRequest,
		LoaderCircle,
		Pencil,
		TriangleAlert,
	} from '@lucide/svelte';
	import { itemLine, mergeState } from '#lib/tray.js';
	import Button from './Button.svelte';
	import InfoCard from './InfoCard.svelte';

	// The change pipeline as seen from a scope: what this user staged, what is
	// in review, and what merged in the last hour and whether it runs. The
	// tray follows this user's own PRs; this card shows everyone's.
	let { scope = {} }: { scope?: { project?: string; namespace?: string } } = $props();

	const projects = $derived.by(() => {
		if (scope.project) return new Set([scope.project]);
		if (scope.namespace) return new Set([inventory.projectOf(scope.namespace)]);
		return null;
	});
	const inScope = (project?: string) => !!project && (!projects || projects.has(project));

	const staged = $derived(drafts.drafts.filter((d) => d.draft.count > 0 && inScope(d.project)));
	const open = $derived(inventory.proposals.filter((p) => inScope(p.project)));

	const merges = $derived(
		inventory.taskFeed.filter((t) => t.kind === 'merge' && inScope(t.project)).slice(0, 3),
	);

	// Each merge is placed by mergeState against its project's history, which
	// the poll re-reads, so a merge missing from the first read still lands.
	// Only a project with a GitOps row can place one: the platform has none
	// here, and a stager without platform read is refused its history.
	const gitOpsOf = (project?: string) => inventory.projects.find((p) => p.name === project)?.gitOps;
	const historyKey = $derived(
		[...new Set(merges.filter((t) => gitOpsOf(t.project)).map((t) => t.project ?? ''))]
			.sort()
			.join(' '),
	);
	const history = resource<Record<string, Commit[]>>(
		() => historyKey,
		async (k) => {
			const out: Record<string, Commit[]> = {};
			await Promise.all(
				(k ? k.split(' ') : []).map(async (p) => {
					try {
						out[p] = await api.history(p);
					} catch {
						// Unreadable history: the merge shows without a state.
					}
				}),
			);
			return out;
		},
		{ poll: 30000 },
	);
	const STATE = {
		live: { tone: 'ok', text: 'Live' },
		refused: { tone: 'danger', text: 'Refused' },
		applying: { tone: 'info', text: 'Applying' },
	} as const;

	const merged = $derived(
		merges.map((t) => {
			const g = gitOpsOf(t.project);
			const commits = history.data?.[t.project ?? ''];
			const hash = commits?.find((c) => c.prNumber === t.prNumber)?.hash;
			const m = hash
				? mergeState(
						g,
						hash,
						commits!.map((c) => c.hash),
					).kind
				: 'merged';
			return { t, state: m === 'merged' ? null : STATE[m] };
		}),
	);

	const empty = $derived(!staged.length && !open.length && !merged.length);
	const allHref = $derived(scope.project ? changesHref(scope.project) : '/changes');
</script>

<InfoCard title="Changes in this scope">
	{#snippet action()}
		<Button variant="link" size="sm" href={allHref}>All changes</Button>
	{/snippet}
	{#if empty}
		<p class="px-card py-2 text-xs text-ink-muted">
			Nothing staged, in review or merged in the last hour here.
		</p>
	{:else}
		<ul class="divide-y divide-line-soft text-xs">
			{#each staged as d (d.project)}
				<li class="flex items-start gap-3 px-card py-2.5">
					<Pencil size={14} class="mt-0.5 shrink-0 text-accent-ink" />
					<div class="min-w-0 flex-1">
						<p class="text-ink"><strong>Staged by you</strong> in {d.project}</p>
						{#each d.draft.items.slice(0, 2) as it (it.kind + it.namespace + it.name)}
							<p class="truncate text-ink-soft">{it.name}: {itemLine(it)}</p>
						{/each}
						{#if d.draft.items.length > 2}
							<p class="text-ink-muted">and {d.draft.items.length - 2} more</p>
						{/if}
					</div>
					<Button
						size="sm"
						variant="outline"
						onclick={() => (ui.modal = { kind: 'propose', project: d.project })}>Review</Button
					>
				</li>
			{/each}
			{#each open as p (p.project + p.prNumber)}
				{@const appr = approvalLine(p)}
				<li class="flex items-start gap-3 px-card py-2.5">
					<GitPullRequest size={14} class="mt-0.5 shrink-0 text-accent-ink" />
					<div class="min-w-0 flex-1">
						<p class="text-ink">
							<strong>PR #{p.prNumber}</strong>{p.mine ? ', yours' : p.by ? ` by ${p.by}` : ''},
							open
						</p>
						<p class="truncate text-ink-soft">
							{p.title ?? ''}{appr ? `${p.title ? '. ' : ''}${appr.text}.` : ''}
						</p>
					</div>
					<Button size="sm" variant="outline" href={p.prURL} target="_blank" class="gap-1"
						>Open in forge <ExternalLink size={12} class="inline" /></Button
					>
				</li>
			{/each}
			{#each merged as { t, state } (`${t.project}#${t.prNumber}@${t.at}`)}
				<li class="flex items-start gap-3 px-card py-2.5">
					{#if state?.tone === 'danger'}
						<TriangleAlert size={14} class="mt-0.5 shrink-0 text-danger-ink" />
					{:else if state?.tone === 'info'}
						<LoaderCircle size={14} class="mt-0.5 shrink-0 text-accent-ink" />
					{:else}
						<Check size={14} class="mt-0.5 shrink-0 text-ok-ink" />
					{/if}
					<div class="min-w-0 flex-1">
						<p class="truncate text-ink"><strong>PR #{t.prNumber}</strong> {t.title ?? ''}</p>
						<p class="text-ink-soft">
							Merged {relativeAge(t.at)}.{#if state}<span class={TONE_TEXT[state.tone]}
									>{` ${state.text}.`}</span
								>{/if}
						</p>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</InfoCard>
