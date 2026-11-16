import { describe, expect, it } from 'vitest';
import { provider, returnPath, signInView } from './signin';

const oidc = { sso: false, ssoPending: false, oidc: 'Example ID' };
const openshift = { sso: true, ssoPending: false };
const pending = { sso: true, ssoPending: true };
const none = { sso: false, ssoPending: false };
const at = (path: string) => new URL(path, 'https://console.example.com');

describe('provider', () => {
	it('prefers OIDC, then a ready OpenShift sign-in', () => {
		expect(provider({ ...oidc, sso: true })?.start).toBe('/api/auth/oidc');
		expect(provider(openshift)).toEqual({ name: 'OpenShift', start: '/api/auth/openshift' });
	});
	it('has none while OpenShift sign-in is pending, or none is configured', () => {
		expect(provider(pending)).toBeNull();
		expect(provider(none)).toBeNull();
	});
});

describe('returnPath', () => {
	it('keeps the page, its query and hash, minus the screen markers', () => {
		expect(returnPath(at('/compute/vms/ns/web?tab=disks&sso_error=1#x'))).toBe(
			'/compute/vms/ns/web?tab=disks#x',
		);
		expect(returnPath(at('/changes?signed_out&token'))).toBe('/changes');
	});
	it('is empty for the root', () => {
		expect(returnPath(at('/'))).toBe('');
		expect(returnPath(at('/?signed_out'))).toBe('');
	});
});

describe('signInView', () => {
	it('redirects to a ready provider, carrying the page to return to', () => {
		expect(signInView(oidc, at('/'))).toEqual({
			kind: 'redirect',
			name: 'Example ID',
			to: '/api/auth/oidc',
		});
		expect(signInView(openshift, at('/compute/vms/ns/web'))).toEqual({
			kind: 'redirect',
			name: 'OpenShift',
			to: '/api/auth/openshift?return=%2Fcompute%2Fvms%2Fns%2Fweb',
		});
	});
	it('shows one button after a failed sign-in, never a redirect', () => {
		expect(signInView(oidc, at('/?sso_error=1'))).toMatchObject({
			kind: 'button',
			reason: 'failed',
			to: '/api/auth/oidc',
		});
	});
	it('shows one button after a sign-out, never a redirect', () => {
		expect(signInView(openshift, at('/?signed_out'))).toMatchObject({
			kind: 'button',
			reason: 'signed-out',
		});
	});
	it('shows one button instead of redirecting twice in a row', () => {
		expect(signInView(oidc, at('/'), { recent: true })).toMatchObject({
			kind: 'button',
			reason: 'repeat',
		});
	});
	it('shows one button right after a sign-out on this page, before ?signed_out lands', () => {
		expect(signInView(oidc, at('/storage'), { signedOut: true })).toMatchObject({
			kind: 'button',
			reason: 'signed-out',
		});
	});
	it('shows one button when the tab cannot record its redirects', () => {
		expect(signInView(oidc, at('/'), { noStorage: true })).toMatchObject({
			kind: 'button',
			reason: 'no-storage',
		});
	});
	it('offers the token form when no provider is ready', () => {
		for (const m of [none, pending, null])
			expect(signInView(m, at('/')), JSON.stringify(m)).toEqual({ kind: 'token' });
	});
	it('offers the token form on ?token, provider or not', () => {
		for (const path of ['/?token', '/?token&sso_error=1', '/?token&signed_out'])
			expect(signInView(oidc, at(path)), path).toEqual({ kind: 'token' });
	});
});
