<script lang="ts">
	import { api } from '$lib/api';
	import ErrorNote from './ErrorNote.svelte';
	import FormField from './FormField.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import TextInput from './TextInput.svelte';

	// Adopt an EXISTING labeled-but-repoless project into GitOps: name and
	// namespaces are cluster facts, so this only creates the repo and stamps the
	// annotations. recover reuses the flow for a lost/moved repo; the backend
	// decides, only the wording differs here.
	let {
		project,
		namespaces,
		recover = false,
		onclose,
		onstaged,
	}: {
		project: string;
		namespaces: string[];
		recover?: boolean;
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

<Modal title={recover ? `Recover repo for "${project}"` : `Attach repo to "${project}"`} {onclose}>
	<div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4 text-sm">
		{#if recover}
			<p class="text-ink-soft">
				This project points at a repo the forge no longer serves. If the repo is lost, recovering
				re-creates it empty; if it actually lives on the current forge under the same name (the
				forge host changed), recovering stages the manifests that re-point the project instead. Your
				workloads keep running either way, and nothing syncs until something merges. After a
				re-create, use "Adopt into git" on the namespaces so the first merge restores everything
				running (the Changes panel warns while anything is left out). Refused if the repo still
				resolves as configured.
			</p>
		{:else}
			<p class="text-ink-soft">
				This project's namespaces exist in the cluster but aren't backed by a git repo. Adopting
				creates the tenant repo and brings the namespaces under GitOps.
			</p>
		{/if}
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
		<FormField label="Owners (optional)">
			<TextInput bind:value={owners} placeholder="alice bob" data-autofocus />
			<span class="mt-1 block text-[11px] text-ink-faint"
				>Usernames granted admin on the project's namespaces (space/comma separated).</span
			>
		</FormField>
		<p class="rounded bg-inset px-3 py-2 text-xs text-ink-muted">
			Creates the tenant repo now, and stages each namespace (with the <code>dotvirt.io/repo</code>
			annotation){#if owners.trim()}
				+ an owners admin grant{/if} into the platform repo. After the PR merges, the project's VMs appear
			as untracked — adopt them with “Adopt N untracked”.
		</p>
		<ErrorNote {error} />
	</div>
	{#snippet footer()}
		<StageFooter
			label="Attach repo"
			summary={`Creates repo “${project}”; stages ${namespaces.length} namespace annotation${namespaces.length === 1 ? '' : 's'} → platform repo`}
			{submitting}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
