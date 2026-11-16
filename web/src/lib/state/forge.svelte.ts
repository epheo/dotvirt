import { api, type ForgeSession } from '#lib/api.js';
import { connectURL, REPEAT_WINDOW_MS, shouldChain, type ConnectHold } from '#lib/forgeconnect.js';
import { friendlyError } from '#lib/format.js';

// The tab's last automatic connect, against a forge round trip that keeps
// coming back unconnected.
const CONNECTED_AT = 'dotvirt.forge-connect-at';

function readHold(): ConnectHold {
	try {
		const at = Number(sessionStorage.getItem(CONNECTED_AT));
		return { recent: at > 0 && Date.now() - at < REPEAT_WINDOW_MS };
	} catch {
		return { noStorage: true };
	}
}

function markStarted(): boolean {
	try {
		sessionStorage.setItem(CONNECTED_AT, String(Date.now()));
		return true;
	} catch {
		return false;
	}
}

// The signed-in user's own forge account, when pull requests go through it.
class ForgeConnection {
	status = $state<ForgeSession | null>(null);
	busy = $state(false);
	error = $state('');

	async load() {
		try {
			this.status = await api.forgeSession();
		} catch {
			// The menu offers nothing until the next read answers.
		}
	}

	// The first sign-in of a console session connects the forge account on its
	// own, once; page is where it comes back to.
	async chainOnce(page: string) {
		await this.load();
		if (!shouldChain(this.status, readHold()) || !markStarted()) return;
		window.location.assign(connectURL(page));
	}

	connect(page: string) {
		window.location.assign(connectURL(page));
	}

	async disconnect() {
		if (this.busy) return;
		this.busy = true;
		this.error = '';
		try {
			await api.forgeDisconnect();
			await this.load();
		} catch (e) {
			this.error = friendlyError(e);
		} finally {
			this.busy = false;
		}
	}

	reset() {
		this.status = null;
		this.error = '';
	}
}

export const forge = new ForgeConnection();
