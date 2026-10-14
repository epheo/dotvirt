<script lang="ts" module>
	export function generatePassword(): string {
		// Unambiguous alphanumerics, 16 chars (about 92 bits): strong enough that
		// the bcrypt hash committed to git is useless to an offline attacker.
		const chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789';
		const buf = new Uint32Array(16);
		crypto.getRandomValues(buf);
		return Array.from(buf, (n) => chars[n % chars.length]).join('');
	}
</script>

<script lang="ts">
	import FormField from '../FormField.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		user = $bindable(),
		password = $bindable(),
		sshKey = $bindable(),
	}: { user: string; password: string; sshKey: string } = $props();
</script>

<div class="space-y-4">
	<p class="text-xs text-ink-muted">
		Guest login, injected at first boot. Cloud images ship with locked passwords, so one is
		generated for console access.
	</p>
	<FormField label="cloud-init user">
		<TextInput bind:value={user} placeholder="cloud-user" />
	</FormField>
	<FormField label="Console password">
		<div class="flex gap-2">
			<TextInput bind:value={password} class="min-w-0 flex-1 font-mono" />
			<button
				type="button"
				onclick={() => (password = generatePassword())}
				class="rounded border border-line px-2.5 text-xs text-ink-soft hover:bg-inset-strong"
				>Regenerate</button
			>
		</div>
		<p class="mt-1 text-xs text-ink-faint">
			Git stores only a hash. The password is shown once after staging and can never be displayed
			again. Clear the field to disable password login.
		</p>
	</FormField>
	<FormField label="SSH public key (optional)">
		<TextInput bind:value={sshKey} placeholder="ssh-ed25519 AAAA…" />
		<p class="mt-1 text-xs text-ink-faint">
			With a key set, the password works on the console only; SSH password login stays off.
		</p>
	</FormField>
</div>
