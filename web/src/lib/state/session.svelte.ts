import { api, HttpError, retryDelay, Unauthorized, type User } from '$lib/api';
import { friendlyError } from '$lib/format';
import { resource, type Resource } from '$lib/resource.svelte';

// The signed-in caller. Every API call runs under this user's own token; the
// layout registers the api layer's one 401 sink to clear it (plus the other
// stores) so any expired session drops the app back to Login.
class Session {
	user = $state<User | null>(null);
	checking = $state(true);
	// The boot probe's last non-401 failure while it keeps retrying. answered
	// tells a server error (it replied) from no response at all.
	bootError = $state<{ answered: boolean; detail: string } | null>(null);
	#wake: (() => void) | null = null;

	// Only a 401 means "sign in": anything else (backend restarting, network
	// blip) is retried with backoff, so a deploy never bounces a valid session
	// to the login screen.
	async check() {
		for (let attempt = 1; ; attempt++) {
			try {
				this.user = await api.me();
				break;
			} catch (e) {
				if (e instanceof Unauthorized) {
					this.user = null;
					break;
				}
				this.bootError = { answered: e instanceof HttpError, detail: friendlyError(e) };
				await new Promise<void>((resolve) => {
					const id = setTimeout(resolve, retryDelay(attempt));
					this.#wake = () => {
						clearTimeout(id);
						resolve();
					};
				});
				this.#wake = null;
			}
		}
		this.bootError = null;
		this.checking = false;
	}

	// Skip the backoff wait and probe now.
	retryNow() {
		this.#wake?.();
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
