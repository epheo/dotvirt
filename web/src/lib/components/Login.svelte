<script lang="ts">
	import { page } from '$app/state';
	import { api, type User } from '$lib/api';

	let { onlogin }: { onlogin: (user: User) => void } = $props();

	let token = $state('');
	let busy = $state(false);
	let error = $state('');
	// SSO is offered once the backend confirms it's configured; the token form is
	// always there (vanilla Kubernetes, ServiceAccounts, or an OAuth outage).
	let sso = $state(false);
	$effect(() => {
		api
			.authMethods()
			.then((m) => (sso = m.sso))
			.catch(() => {});
	});
	// The OAuth callback bounces here with ?sso_error=1 on any failure (the
	// detail is server-logged, never shown — it can carry endpoint internals).
	const ssoError = $derived(page.url.searchParams.get('sso_error') !== null);

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
			dotvirt acts as you — you see only the projects your cluster permissions allow.
		</p>

		{#if ssoError}
			<p
				class="mb-3 rounded border border-danger-soft bg-danger-soft/60 px-3 py-2 text-sm text-danger-ink"
			>
				OpenShift sign-in failed. Try again, or paste a token below.
			</p>
		{/if}

		{#if sso}
			<a
				href="/api/auth/openshift"
				class="mb-4 block w-full rounded bg-accent px-4 py-2 text-center text-sm font-medium text-white hover:bg-accent-hover"
			>
				Sign in with OpenShift
			</a>
			<div class="mb-3 flex items-center gap-2 text-xs text-ink-faint">
				<span class="h-px flex-1 bg-line"></span>
				or paste a token
				<span class="h-px flex-1 bg-line"></span>
			</div>
		{/if}

		<textarea
			bind:value={token}
			placeholder="sha256~…  or  eyJhbGci…"
			rows="4"
			autocomplete="off"
			spellcheck="false"
			class="w-full rounded border border-line-strong px-3 py-2 font-mono text-xs break-all"
		></textarea>

		{#if error}
			<p class="mt-2 text-sm text-danger">{error}</p>
		{/if}

		<button
			type="submit"
			disabled={busy || !token.trim()}
			class="mt-3 w-full rounded {sso
				? 'border border-line bg-inset text-ink-soft hover:bg-inset-strong'
				: 'bg-accent text-white hover:bg-accent'} px-4 py-2 text-sm font-medium disabled:opacity-40"
		>
			{busy ? 'Signing in…' : 'Sign in with token'}
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
