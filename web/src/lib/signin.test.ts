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
		expect(signInView(oidc, at('/'), false)).toEqual({
			kind: 'redirect',
			name: 'Example ID',
			to: '/api/auth/oidc',
		});
		expect(signInView(openshift, at('/compute/vms/ns/web'), false)).toEqual({
			kind: 'redirect',
			name: 'OpenShift',
			to: '/api/auth/openshift?return=%2Fcompute%2Fvms%2Fns%2Fweb',
		});
	});
	it('shows one button after a failed sign-in, never a redirect', () => {
		expect(signInView(oidc, at('/?sso_error=1'), false)).toMatchObject({
			kind: 'button',
			reason: 'failed',
			to: '/api/auth/oidc',
		});
	});
	it('shows one button after a sign-out, never a redirect', () => {
		expect(signInView(openshift, at('/?signed_out'), false)).toMatchObject({
			kind: 'button',
			reason: 'signed-out',
		});
	});
	it('shows one button instead of redirecting twice in a row', () => {
		expect(signInView(oidc, at('/'), true)).toMatchObject({ kind: 'button', reason: 'repeat' });
	});
	it('offers the token form when no provider is ready', () => {
		for (const m of [none, pending, null])
			expect(signInView(m, at('/'), false), JSON.stringify(m)).toEqual({ kind: 'token' });
	});
	it('offers the token form on ?token, provider or not', () => {
		for (const path of ['/?token', '/?token&sso_error=1', '/?token&signed_out'])
			expect(signInView(oidc, at(path), false), path).toEqual({ kind: 'token' });
	});
});
