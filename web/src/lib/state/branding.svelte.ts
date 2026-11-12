import { api, type Branding } from '#lib/api.js';

// A provider's dress for the console, read before sign-in. The served shell
// carries it (branding.Settings.Shell), read at init so the first paint is
// already dressed; GET /api/branding answers where no shell does (dev on Vite).
// Null (not read yet, or the read failed) keeps dotvirt's own.
class BrandingStore {
	value = $state.raw<Branding | null>(shellBranding());
	// The shell also carries the title and the stylesheet.
	readonly fromShell = this.value !== null;
	get name() {
		return this.value?.productName || 'dotvirt';
	}
	async load() {
		if (this.fromShell) return;
		try {
			this.value = await api.branding();
		} catch {
			this.value = null;
		}
	}
}

function shellBranding(): Branding | null {
	if (typeof document === 'undefined') return null;
	const raw = document.getElementById('dotvirt-branding')?.textContent;
	if (!raw) return null;
	try {
		return JSON.parse(raw) as Branding;
	} catch {
		return null;
	}
}

export const branding = new BrandingStore();
