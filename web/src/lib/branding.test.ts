import { describe, expect, it } from 'vitest';
import { isConsoleLink } from './branding';

describe('isConsoleLink', () => {
	const origin = 'https://console.example.com';
	it('counts the same origin, whatever the path', () => {
		for (const url of [
			'https://console.example.com',
			'https://console.example.com/',
			'https://console.example.com/compute?x=1#y',
			'https://CONSOLE.example.com/',
			'https://console.example.com:443/',
		])
			expect(isConsoleLink(url, origin), url).toBe(true);
	});
	it('does not count another host, port or scheme', () => {
		for (const url of [
			'https://example.com/',
			'https://other.console.example.com/',
			'https://console.example.com:8443/',
			'http://console.example.com/',
		])
			expect(isConsoleLink(url, origin), url).toBe(false);
	});
	it('leaves an unparsable URL a link', () => {
		for (const url of ['', 'console.example.com', 'not a url'])
			expect(isConsoleLink(url, origin), url).toBe(false);
	});
});
