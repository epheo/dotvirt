// Client-side mirrors of the server's authoritative validation — for
// per-field hints only, never enforcement.

// RFC 1123 label: what the API server enforces on resource names.
export const validName = (s: string) => /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(s) && s.length <= 63;

export const NAME_HINT = 'Lowercase alphanumeric and "-" only, max 63 characters.';

// Shape check only (v4 or v6) — octet ranges and mask width stay server-side.
export const validCIDR = (s: string) =>
	s.includes(':') ? /^[0-9a-fA-F:]+\/\d{1,3}$/.test(s) : /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/.test(s);

export const CIDR_HINT = 'Expected CIDR notation, e.g. 10.20.0.0/24.';

// Shape check only (v4 or v6), no octet-range enforcement.
export const validIP = (s: string) =>
	s.includes(':') ? /^[0-9a-fA-F:]+$/.test(s) : /^(\d{1,3}\.){3}\d{1,3}$/.test(s);
