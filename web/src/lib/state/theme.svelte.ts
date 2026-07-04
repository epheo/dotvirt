import { browser } from '$app/environment';
import { persisted } from './persisted.svelte';

export type ThemeMode = 'light' | 'dark' | 'system';

// The persisted preference is the mode; `resolved` is what's on screen.
// app.html stamps data-theme before first paint (FOUC guard); this store owns
// it from then on. The stamp happens synchronously in the setter/listener —
// before any effect flush — so canvas code that getComputedStyle()s the chart
// vars inside an effect keyed on `resolved` reads post-flip values.
const mode = persisted<ThemeMode>('dotvirt.theme', 'system');
const media = browser ? matchMedia('(prefers-color-scheme: dark)') : null;

let osDark = $state(media?.matches ?? false);
media?.addEventListener('change', (e) => {
	osDark = e.matches;
	stamp();
});

function resolve(m: ThemeMode, dark: boolean): 'light' | 'dark' {
	return m === 'system' ? (dark ? 'dark' : 'light') : m;
}

function stamp() {
	if (browser) document.documentElement.dataset.theme = resolve(mode.value, osDark);
}

class Theme {
	get mode(): ThemeMode {
		return mode.value;
	}
	set mode(m: ThemeMode) {
		mode.value = m;
		stamp();
	}
	get resolved(): 'light' | 'dark' {
		return resolve(mode.value, osDark);
	}
}

export const theme = new Theme();
