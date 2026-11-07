<script lang="ts">
	import { api, type Commit } from '#lib/api.js';
	import { opStands } from '#lib/gitops.js';
	import { relativeAge } from '#lib/format.js';
	import { changesHref } from '#lib/nav.js';
	import { resource } from '#lib/resource.svelte.js';
	import { approvalLine } from '#lib/review.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import { TONE_PILL } from '#lib/status.js';
	import { itemLine } from '#lib/tray.js';
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

	// A merge runs once Argo has applied a revision at or after its commit, and
	// the app is Synced; history orders the two. A standing failed operation
	// on a merge not yet applied reads refused.
	const historyKey = $derived([...new Set(merges.map((t) => t.project ?? ''))].sort().join(' '));
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
	const merged = $derived(
		merges.map((t) => {
			const g = inventory.projects.find((p) => p.name === t.project)?.gitOps;
			const commits = history.data?.[t.project ?? ''];
			const at = commits?.findIndex((c) => c.prNumber === t.prNumber) ?? -1;
			const applied = g?.revision
				? (commits?.findIndex((c) => c.hash.startsWith(g.revision!)) ?? -1)
				: -1;
			const state =
				!g || at < 0
					? null
					: applied >= 0 && at >= applied && g.sync === 'Synced'
						? { tone: 'ok' as const, text: 'Live' }
						: opStands(g)
							? { tone: 'danger' as const, text: 'Refused' }
							: { tone: 'info' as const, text: 'Applying' };
			return { t, state };
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
		<p class="px-3 py-2 text-xs text-ink-muted">
			Nothing staged, in review or merged in the last hour here.
		</p>
	{:else}
		<ul class="divide-y divide-line-soft text-xs">
			{#each staged as d (d.project)}
				<li class="flex items-start gap-3 px-3 py-2">
					<div class="min-w-0 flex-1">
						<p class="text-ink">
							<span class="rounded-full px-1.5 {TONE_PILL.info}">Staged</span>
							by you in {d.project}
						</p>
						{#each d.draft.items.slice(0, 2) as it (it.kind + it.namespace + it.name)}
							<p class="truncate text-ink-soft">{it.name}: {itemLine(it)}</p>
						{/each}
						{#if d.draft.items.length > 2}
							<p class="text-ink-muted">and {d.draft.items.length - 2} more</p>
						{/if}
					</div>
					<Button
						size="sm"
						variant="secondary"
						onclick={() => (ui.modal = { kind: 'propose', project: d.project })}>Review</Button
					>
				</li>
			{/each}
			{#each open as p (p.project + p.prNumber)}
				{@const appr = approvalLine(p)}
				<li class="flex items-start gap-3 px-3 py-2">
					<div class="min-w-0 flex-1">
						<p class="text-ink">
							<span class="rounded-full px-1.5 {TONE_PILL.warn}">In review</span>
							PR #{p.prNumber}{p.mine ? ', yours' : p.by ? ` by ${p.by}` : ''}
						</p>
						<p class="truncate text-ink-soft">
							{p.title ?? ''}{appr ? `${p.title ? '. ' : ''}${appr.text}.` : ''}
						</p>
					</div>
					<Button size="sm" variant="ghost" href={p.prURL} target="_blank">Open in forge</Button>
				</li>
			{/each}
			{#each merged as { t, state } (`${t.project}#${t.prNumber}@${t.at}`)}
				<li class="flex items-start gap-3 px-3 py-2">
					<div class="min-w-0 flex-1">
						<p class="text-ink">
							<span class="rounded-full px-1.5 {TONE_PILL.ok}">Merged</span>
							PR #{t.prNumber}
							{relativeAge(t.at)}{state ? '.' : ''}
							{#if state}<span class="rounded-full px-1.5 {TONE_PILL[state.tone]}"
									>{state.text}</span
								>{/if}
						</p>
						{#if t.title}<p class="truncate text-ink-soft">{t.title}</p>{/if}
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</InfoCard>
