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

<div class="flex h-screen items-center justify-center bg-slate-100">
	<form
		onsubmit={submit}
		class="w-full max-w-md rounded-lg border border-slate-200 bg-white p-6 shadow-sm"
	>
		<h1 class="mb-1 text-xl font-semibold text-slate-800">Sign in to dotvirt</h1>
		<p class="mb-4 text-sm text-slate-500">
			Paste your Kubernetes API token. dotvirt acts as you — you see only the projects your cluster
			permissions allow.
		</p>

		<textarea
			bind:value={token}
			placeholder="sha256~…  or  eyJhbGci…"
			rows="4"
			autocomplete="off"
			spellcheck="false"
			class="w-full rounded border border-slate-300 px-3 py-2 font-mono text-xs break-all"
		></textarea>

		{#if error}
			<p class="mt-2 text-sm text-red-600">{error}</p>
		{/if}

		<button
			type="submit"
			disabled={busy || !token.trim()}
			class="mt-3 w-full rounded bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-500 disabled:opacity-40"
		>
			{busy ? 'Signing in…' : 'Sign in'}
		</button>

		<div class="mt-4 border-t border-slate-100 pt-3 text-xs text-slate-500">
			<p class="mb-1 font-medium text-slate-600">Get a token:</p>
			<pre class="rounded bg-slate-50 px-2 py-1 text-slate-600">oc whoami -t</pre>
			<p class="my-1">or for a ServiceAccount:</p>
			<pre
				class="rounded bg-slate-50 px-2 py-1 text-slate-600">kubectl create token &lt;sa&gt; -n &lt;namespace&gt;</pre>
		</div>
	</form>
</div>
