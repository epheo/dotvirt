import type { ForgeSession } from './api';
import { REPEAT_WINDOW_MS } from './signin';

// The page a connect returns to, re-checked by the backend (SafeReturn).
export function connectURL(page: string): string {
	return `/api/forge/connect?return=${encodeURIComponent(page || '/')}`;
}

// Why the console must not start a connect on its own beyond the session.
// recent: one ran in this tab within REPEAT_WINDOW_MS, so a forge round trip
// that leaves no connection cannot loop. noStorage: the tab cannot record
// that, so nothing would stop a loop: the user connects from the menu instead.
export interface ConnectHold {
	recent?: boolean;
	noStorage?: boolean;
}

// Whether the console connects the user's forge account on its own now: once
// per console session (the backend's asked), never twice in a minute in a tab.
// An abandoned connect is then never restarted this session; the next sign-in
// asks again.
export function shouldChain(s: ForgeSession | null, hold: ConnectHold = {}): boolean {
	return !!s && s.delegated && !s.connected && !s.asked && !hold.recent && !hold.noStorage;
}

export { REPEAT_WINDOW_MS };
