import { api, type User } from '$lib/api';
import { resource, type Resource } from '$lib/resource.svelte';

// The signed-in caller. Every API call runs under this user's own token; the
// layout registers the api layer's one 401 sink to clear it (plus the other
// stores) so any expired session drops the app back to Login.
class Session {
	user = $state<User | null>(null);
	checking = $state(true);

	async check() {
		try {
			this.user = await api.me();
		} catch {
			this.user = null;
		} finally {
			this.checking = false;
		}
	}

	async logout() {
		try {
			await api.logout();
		} catch {
			/* ignore */
		}
		this.user = null;
	}
}

export const session = new Session();

// Which sign-in paths exist, read fresh per mount: the login screen offers
// SSO once the backend confirms it, the admin banner offers to finish an SSO
// whose OAuthClient is still unregistered. Call during component init.
export function authMethods(): Resource<{ sso: boolean; ssoPending: boolean }> {
	return resource(
		() => '',
		() => api.authMethods(),
	);
}
