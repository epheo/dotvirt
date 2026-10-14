<script lang="ts">
	import { Check, Copy } from 'lucide-svelte';
	import Button from '../Button.svelte';
	import Modal from '../Modal.svelte';

	let {
		name,
		user,
		password,
		onclose,
	}: { name: string; user: string; password: string; onclose: () => void } = $props();

	let copied = $state('');
	function copy(label: string, value: string) {
		navigator.clipboard.writeText(value);
		copied = label;
		setTimeout(() => (copied = ''), 1500);
	}
</script>

<!-- Pinned open (no Escape/backdrop/X): the password exists only on this
     screen, so the sole way out is the explicit Done. -->
<Modal title="Guest credentials" dismissable={false} {onclose}>
	<div class="space-y-3 px-5 py-4 text-sm">
		<p class="text-ink-soft">
			{name} is staged; open a PR from “Changes” to create it. Copy the console password now.
		</p>
		<dl class="divide-y divide-line-soft rounded border border-line">
			<div class="flex items-center justify-between gap-3 px-3 py-2">
				<dt class="text-ink-muted">User</dt>
				<dd class="flex items-center gap-2 font-mono text-ink">
					{user || 'image default'}
					{#if user}
						<button
							type="button"
							onclick={() => copy('user', user)}
							aria-label="Copy user"
							class="text-ink-faint hover:text-ink-soft"
							>{#if copied === 'user'}<Check size={13} />{:else}<Copy size={13} />{/if}</button
						>
					{/if}
				</dd>
			</div>
			<div class="flex items-center justify-between gap-3 px-3 py-2">
				<dt class="text-ink-muted">Password</dt>
				<dd class="flex items-center gap-2 font-mono text-ink">
					{password}
					<button
						type="button"
						onclick={() => copy('password', password)}
						aria-label="Copy password"
						class="text-ink-faint hover:text-ink-soft"
						>{#if copied === 'password'}<Check size={13} />{:else}<Copy size={13} />{/if}</button
					>
				</dd>
			</div>
		</dl>
		<p class="rounded border border-warn-soft bg-warn-soft/60 px-3 py-2 text-xs text-warn-ink">
			This is the only time the password is shown. Git keeps a hash, so it cannot be recovered or
			displayed again; if it is lost, stage a new one.
		</p>
	</div>
	{#snippet footer()}
		<Button class="ml-auto" onclick={onclose}>Done</Button>
	{/snippet}
</Modal>
