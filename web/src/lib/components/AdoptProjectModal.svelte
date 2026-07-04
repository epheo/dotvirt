<script lang="ts">
	import { api } from '$lib/api';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

	// Adopt an EXISTING labeled-but-repoless project into GitOps: unlike NewProjectModal
	// the name and namespaces are fixed (they already exist in the cluster), so this
	// only creates the tenant repo and stamps the dotvirt.io/repo annotation onto the
	// project's namespaces — optionally granting owners admin.
	let {
		project,
		namespaces,
		onclose,
		onstaged,
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

<Modal title="Attach repo to “{project}”" {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<p class="text-ink-soft">
			This project's namespaces exist in the cluster but aren't backed by a git repo. Adopting
			creates the tenant repo and brings the namespaces under GitOps.
		</p>
		<div class="rounded border border-line px-3 py-2 text-xs text-ink-muted">
			<div>
				<span class="text-ink-faint">Repo to create:</span>
				<code class="text-ink-soft">{project}</code> (sibling of the platform repo)
			</div>
			<div class="mt-1">
				<span class="text-ink-faint">Namespaces:</span>
				<span class="text-ink-soft">{namespaces.join(', ')}</span>
			</div>
		</div>
		<label class="block">
			<span class="text-ink-soft">Owners <span class="text-ink-faint">(optional)</span></span>
			<input
				bind:value={owners}
				placeholder="alice bob"
				class="mt-1 w-full rounded border border-line-strong px-2 py-1.5"
			/>
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Usernames granted admin on the project's namespaces (space/comma separated).</span
			>
		</label>
		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			Creates the tenant repo now, and stages each namespace (with the <code>dotvirt.io/repo</code>
			annotation){#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. After the PR merges, the project's VMs appear
			as untracked — adopt them with “Adopt N untracked”.
		</p>
		{#if error}
			<pre
				class="rounded bg-danger-soft/60 p-3 text-xs whitespace-pre-wrap text-danger-ink">{error}</pre>
		{/if}
	</div>
	{#snippet footer()}
		<StageFooter label="Attach repo" {submitting} onsubmit={submit} oncancel={onclose} />
	{/snippet}
</Modal>
