<script lang="ts">
	import { X } from 'lucide-svelte';
	import { api } from '$lib/api';

	// Adopt an EXISTING labeled-but-repoless project into GitOps: unlike NewProjectModal
	// the name and namespaces are fixed (they already exist in the cluster), so this
	// only creates the tenant repo and stamps the dotvirt.io/repo annotation onto the
	// project's namespaces — optionally granting owners admin.
	let {
		project,
		namespaces,
		onclose,
		onstaged
	}: {
		project: string;
		namespaces: string[];
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	let owners = $state(''); // space/comma-separated usernames
	let submitting = $state(false);
	let error = $state('');

	const parseOwners = (s: string): string[] =>
		s
			.split(/[\s,]+/)
			.map((o) => o.trim())
			.filter(Boolean);

	async function submit() {
		submitting = true;
		error = '';
		try {
			await api.adoptProject(project, parseOwners(owners));
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
			<h2 class="text-base font-semibold text-slate-800">Attach repo to “{project}”</h2>
			<button onclick={onclose} aria-label="Close" class="text-slate-400 hover:text-slate-700"
				><X size={18} /></button
			>
		</header>
		<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
			<p class="text-slate-600">
				This project's namespaces exist in the cluster but aren't backed by a git repo. Adopting
				creates the tenant repo and brings the namespaces under GitOps.
			</p>
			<div class="rounded border border-slate-200 px-3 py-2 text-xs text-slate-500">
				<div>
					<span class="text-slate-400">Repo to create:</span>
					<code class="text-slate-700">{project}</code> (sibling of the platform repo)
				</div>
				<div class="mt-1">
					<span class="text-slate-400">Namespaces:</span>
					<span class="text-slate-700">{namespaces.join(', ')}</span>
				</div>
			</div>
			<label class="block">
				<span class="text-slate-600">Owners <span class="text-slate-400">(optional)</span></span>
				<input
					bind:value={owners}
					placeholder="alice bob"
					class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
				/>
				<span class="mt-1 block text-[11px] text-slate-400"
					>Usernames granted admin on the project's namespaces (space/comma separated).</span
				>
			</label>
			<p class="rounded bg-slate-50 px-3 py-2 text-xs text-slate-500">
				Creates the tenant repo now, and stages each namespace (with the <code>dotvirt.io/repo</code>
				annotation){#if owners.trim()} + an owners admin grant{/if} into the platform repo. After the
				PR merges, the project's VMs appear as untracked — adopt them with “Adopt N untracked”.
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
				disabled={submitting}
				class="rounded bg-blue-600 px-4 py-1.5 text-sm font-medium text-white disabled:bg-slate-300"
			>
				{submitting ? 'Staging…' : 'Attach repo'}
			</button>
		</footer>
	</div>
</div>
