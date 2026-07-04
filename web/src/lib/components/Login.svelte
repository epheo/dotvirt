<script lang="ts">
	import { api, type User } from '$lib/api';

	let { onlogin }: { onlogin: (user: User) => void } = $props();

	let token = $state('');
	let busy = $state(false);
	let error = $state('');

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (!token.trim()) return;
		busy = true;
		error = '';
		try {
			const user = await api.login(token.trim());
			onlogin(user);
		} catch {
			error = 'That token was rejected. Check it and try again.';
		} finally {
			busy = false;
		}
	}
</script>

<div class="flex h-screen items-center justify-center bg-inset-strong">
	<form
		onsubmit={submit}
		class="w-full max-w-md rounded-lg border border-line bg-panel p-6 shadow-sm"
	>
		<h1 class="mb-1 text-xl font-semibold text-ink">Sign in to dotvirt</h1>
		<p class="mb-4 text-sm text-ink-muted">
			Paste your Kubernetes API token. dotvirt acts as you — you see only the projects your cluster
			permissions allow.
		</p>

		<textarea
			bind:value={token}
			placeholder="sha256~…  or  eyJhbGci…"
			rows="4"
			autocomplete="off"
			spellcheck="false"
			class="w-full rounded border border-line-strong px-3 py-2 font-mono text-xs break-all"
		></textarea>

		{#if error}
			<p class="mt-2 text-sm text-red-600">{error}</p>
		{/if}

		<button
			type="submit"
			disabled={busy || !token.trim()}
			class="mt-3 w-full rounded bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent disabled:opacity-40"
		>
			{busy ? 'Signing in…' : 'Sign in'}
		</button>

		<div class="mt-4 border-t border-line-soft pt-3 text-xs text-ink-muted">
			<p class="mb-1 font-medium text-ink-soft">Get a token:</p>
			<pre class="rounded bg-inset px-2 py-1 text-ink-soft">oc whoami -t</pre>
			<p class="my-1">or for a ServiceAccount:</p>
			<pre
				class="rounded bg-inset px-2 py-1 text-ink-soft">kubectl create token &lt;sa&gt; -n &lt;namespace&gt;</pre>
		</div>
	</form>
</div>
