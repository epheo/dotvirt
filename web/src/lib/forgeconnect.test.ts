import { describe, expect, it } from 'vitest';
import { connectURL, shouldChain } from './forgeconnect';

const off = { delegated: false, connected: false, asked: false };
const fresh = { delegated: true, connected: false, asked: false };

describe('shouldChain', () => {
	it('connects a delegated, unconnected session once', () => {
		expect(shouldChain(fresh)).toBe(true);
	});
	it('never without delegation, a session, or once connected', () => {
		expect(shouldChain(null)).toBe(false);
		expect(shouldChain(off)).toBe(false);
		expect(shouldChain({ ...fresh, connected: true, login: 'a' })).toBe(false);
	});
	it('never again in a session that already started one (an abandoned connect)', () => {
		expect(shouldChain({ ...fresh, asked: true })).toBe(false);
	});
	it('never twice within the window, nor without storage to count it', () => {
		expect(shouldChain(fresh, { recent: true })).toBe(false);
		expect(shouldChain(fresh, { noStorage: true })).toBe(false);
	});
});

describe('connectURL', () => {
	it('carries the page to come back to, encoded', () => {
		expect(connectURL('/storage?x=1')).toBe('/api/forge/connect?return=%2Fstorage%3Fx%3D1');
		expect(connectURL('')).toBe('/api/forge/connect?return=%2F');
	});
});
