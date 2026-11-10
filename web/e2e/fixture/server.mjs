// Hermetic fixture backend: serves the built SPA plus a scripted /api that
// replays scenario states (scenarios.mjs), including the WS inventory stream.
// The UI runs against it exactly as against the Go backend, with no cluster,
// forge, or Argo — so CI can hold the app in corner-case states on demand.
//
//   node e2e/fixture/server.mjs        (expects ../build from `npm run build`)
//   FIXTURE_SCENARIO=scale node e2e/fixture/server.mjs   (start in another scenario)
//
// Control plane (no auth, test-only):
//   POST /__fixture/scenario/{name}    switch state; resets session drafts
//   POST /__fixture/ws-drop            close every live inventory socket
//   POST /__fixture/ws-refuse/{n}      destroy the next n WS handshakes pre-open
//   POST /__fixture/login-status/{s}   next POST /api/login answers status s
//   POST /__fixture/fail/{plane}/{n}   the next n calls of a plane answer 503 (events, logout)
//   GET  /__fixture/scenario           current scenario name

import { createReadStream, existsSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { WebSocketServer } from 'ws';
import { scenarios } from './scenarios.mjs';
import { handleCreate } from './create.mjs';

const buildDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'build');
if (!existsSync(join(buildDir, 'index.html'))) {
	console.error(`fixture: no SPA build at ${buildDir} — run \`npm run build\` first`);
	process.exit(1);
}

const port = Number(process.env.FIXTURE_PORT ?? 4173);
const COOKIE = 'dv_fixture=1';

// The merged change history serves, as a full hash: the API refuses anything shorter.
const MERGE_HASH = 'ab12cd34ef' + '0'.repeat(30);
const INITIAL_HASH = '99fe210aaa99fe210aaa99fe210aaa99fe210aaa';
const initialCommit = () => ({
	hash: INITIAL_HASH,
	shortHash: INITIAL_HASH.slice(0, 8),
	message: 'initial import',
	title: 'initial import',
	author: 'admin',
	when: new Date(Date.now() - 9e7).toISOString(),
});

const mergeCommit = (project) => ({
	hash: MERGE_HASH,
	shortHash: MERGE_HASH.slice(0, 8),
	message: `Merge pull request 'web-2: add data disk' (#40) from dotvirt/proposed/admin/${project} into main`,
	title: 'web-2: add data disk',
	author: 'admin',
	when: new Date(Date.now() - 6e6).toISOString(),
	merge: true,
	prNumber: 40,
	prURL: `https://forge.example/dotvirt/${project}/pulls/40`,
});
const WEB2_BASE =
	'kind: VirtualMachine\nmetadata:\n  name: web-2\n  namespace: web-prod\nspec:\n  disks:\n  - name: root\n';

// ── networking: rules, traffic and history derived from the scenario ──────────

// The namespaces a network's VMs live in, by its route key.
function networkNamespaces(s, key) {
	const n = (s.networks?.networks ?? []).find(
		(x) => x.attachRef === key || (x.scope === 'shared' && x.name === key),
	);
	if (!n) return null;
	return n.scope === 'shared' ? (n.namespaces ?? []) : [n.namespace];
}

// What binds some namespaces, in the evaluator's order.
function bindingsFor(s, nss) {
	const rank = { admin: 0, dfw: 1, baseline: 2, gateway: 3, egressip: 4, route: 5 };
	const on = (p) =>
		p.namespace
			? nss.includes(p.namespace)
			: p.kind === 'admin' ||
				p.kind === 'baseline' ||
				(p.namespaces ?? []).some((n) => nss.includes(n));
	return (s.policies?.policies ?? [])
		.filter(on)
		.sort((a, b) => rank[a.kind] - rank[b.kind] || (a.priority ?? 0) - (b.priority ?? 0))
		.map((policy) => ({
			policy,
			conditional:
				policy.kind === 'dfw' && policy.target !== 'all pods' && policy.target !== 'all VMs',
		}));
}

// One workload's effective policy: pod selectors resolve against app=<vm>.
function effectiveFor(s, ns, vm) {
	const b = bindingsFor(s, [ns])
		.filter(
			(x) =>
				x.policy.kind !== 'dfw' ||
				x.policy.target === 'all pods' ||
				x.policy.target === 'all VMs' ||
				x.policy.target === `app=${vm}`,
		)
		.map((x) => ({ ...x, conditional: false }));
	const of = (...kinds) => b.filter((x) => kinds.includes(x.policy.kind));
	return {
		namespace: ns,
		vm,
		labels: { app: vm },
		eastWest: of('admin', 'dfw', 'baseline'),
		gateway: of('gateway'),
		snat: of('egressip'),
		routes: of('route'),
	};
}

// A stable traffic figure per VM name, so screenshots do not move.
function trafficOf(name) {
	let h = 7;
	for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 997;
	return (40 + (h % 160)) * 1e6;
}

const networkCommits = (project, name) => [
	{
		hash: 'c42c42c42c' + '0'.repeat(30),
		shortHash: 'c42c42c4',
		message: `Merge pull request 'Attach two VMs to ${name}' (#42)`,
		title: `Attach two VMs to ${name}`,
		author: 'admin',
		when: new Date(Date.now() - 3 * 864e5).toISOString(),
		merge: true,
		prNumber: 42,
		prURL: `https://forge.example/dotvirt/${project}/pulls/42`,
	},
	{
		hash: 'c39c39c39c' + '0'.repeat(30),
		shortHash: 'c39c39c3',
		message: `Merge pull request 'Move ${name} to a /16' (#39)`,
		title: `Move ${name} to a /16`,
		author: 'admin',
		when: new Date(Date.now() - 8 * 864e5).toISOString(),
		merge: true,
		prNumber: 39,
		prURL: `https://forge.example/dotvirt/${project}/pulls/39`,
	},
];

const state = {
	scenario: scenarios[process.env.FIXTURE_SCENARIO ?? 'base'],
	// Per-project items staged through this session's POSTs, merged over the
	// scenario's own drafts so staging flows work end to end.
	staged: new Map(),
	loginStatus: 0, // one-shot override for POST /api/login
	wsRefuse: 0, // destroy the next n WS handshakes before the socket opens
	sockets: new Set(),
	uploads: new Map(), // "ns/name" -> DataVolume phase, or an import's phases, for the status poll
	fail: new Map(), // plane -> reads left to fail, so a test sees the error and the retry
};

// failing spends one injected failure of a plane, if any are left.
function failing(plane) {
	const n = state.fail.get(plane) ?? 0;
	if (n > 0) state.fail.set(plane, n - 1);
	return n > 0;
}

const MIME = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'text/javascript',
	'.css': 'text/css',
	'.json': 'application/json',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.ico': 'image/x-icon',
	'.woff2': 'font/woff2',
};

function sendJSON(res, status, body) {
	const data = JSON.stringify(body);
	res.writeHead(status, { 'Content-Type': 'application/json' });
	res.end(data);
}

function sendText(res, status, text) {
	res.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
	res.end(text);
}

function authed(req) {
	return (req.headers.cookie ?? '').includes(COOKIE);
}

function readBody(req) {
	return new Promise((resolve) => {
		let data = '';
		req.on('data', (c) => (data += c));
		req.on('end', () => {
			try {
				resolve(data ? JSON.parse(data) : {});
			} catch {
				resolve(null);
			}
		});
	});
}

function broadcastInventory() {
	const frame = JSON.stringify(state.scenario.inventory);
	for (const ws of state.sockets) ws.send(frame);
}

// ── drafts: scenario baseline + runtime staged items ───────────────────────────

function draftFor(project) {
	const fixed = state.scenario.drafts?.[project];
	const staged = state.staged.get(project) ?? [];
	const items = [...(fixed?.items ?? []), ...staged];
	return {
		base: 'main',
		branch: items.length ? `dotvirt/proposed/admin/${project}` : '',
		...fixed,
		count: items.length,
		items,
		...proposeDefaults(items),
	};
}

// changeset.valueTitle: one VM edit of plain values reads "web-1: 4 vCPU, 8 GiB memory".
function valueTitle(items) {
	const it = items.length === 1 && items[0].kind === 'edit' ? items[0] : null;
	const cs = it?.changes ?? [];
	if (!it || (it.resource && it.resource !== 'vm') || !cs.length || cs.length > 3) return null;
	const parts = [];
	for (const c of cs) {
		if (c.action !== 'change' || !c.to) return null;
		if (c.field === 'CPU' || c.field === 'Instance type')
			parts.push(/^\d+$/.test(c.to) ? `${c.to} vCPU` : c.to);
		else if (c.field === 'Memory') parts.push(`${c.to.replace(/(Ki|Mi|Gi|Ti)$/, ' $1B')} memory`);
		else if (c.field === 'Power') parts.push(`power ${c.to.toLowerCase()}`);
		else return null;
	}
	return `${it.name}: ${parts.join(', ')}`;
}

// The backend's untitled-propose defaults, shown in the form as suggestions.
// A JS twin of changeset.defaultTitle / prBody, close enough for the UI.
function proposeDefaults(items) {
	if (!items.length) return {};
	const verbs = { edit: 'Update', create: 'Create', delete: 'Delete' };
	const kinds = new Set(items.map((it) => it.kind));
	const names = items.map((it) => it.name).join(', ');
	const defaultTitle =
		valueTitle(items) ??
		(kinds.size === 1 ? `${verbs[items[0].kind]} ${names}` : `${items.length} changes: ${names}`);
	const lines = items.flatMap((it) => [
		'',
		`**${it.kind} ${it.namespace}/${it.name}**`,
		...(it.changes ?? []).map((c) =>
			c.action === 'add'
				? `- ${c.field}: + ${c.to}`
				: c.action === 'remove'
					? `- ${c.field}: - ${c.from}`
					: `- ${c.field}: ${c.from ?? ''} -> ${c.to}`,
		),
	]);
	return {
		defaultTitle,
		defaultBody: ['## Changes', ...lines, '', '---', 'Proposed from dotvirt by admin.'].join('\n'),
	};
}

function projectOf(namespace) {
	for (const p of state.scenario.inventory.projects) {
		if (p.namespaces.some((n) => n.namespace === namespace)) return p;
	}
	return null;
}

function stage(res, namespace, name, item) {
	const p = projectOf(namespace);
	if (!p) return sendText(res, 404, 'namespace not found in any visible project');
	if (!p.repo) return sendText(res, 503, 'project has no usable repo');
	stageInto(p.name, { namespace, name, ...item });
	sendJSON(res, 200, draftFor(p.name));
}

// One entry per object, replaced on every stage, like the backend's draft store.
function stageInto(project, item) {
	const same = (i) =>
		(i.resource ?? '') === (item.resource ?? '') &&
		i.namespace === item.namespace &&
		i.name === item.name;
	const items = (state.staged.get(project) ?? []).filter((i) => !same(i));
	items.push(item);
	state.staged.set(project, items);
}

// Human-readable change rows from a VMEdit body, with the backend's labels and
// restart flags (manifest.ChangesForEdit), against the VM as git declares it.
function editChanges(body, vm) {
	const changes = [];
	const change = (field, from, to, restart = false) => {
		if (String(from ?? '') !== String(to))
			changes.push({
				field,
				action: 'change',
				from: from === undefined ? undefined : String(from),
				to: String(to),
				restart,
			});
	};
	if (body.power) change('Power', vm?.power, body.power);
	if (body.cpuCores) change('CPU', vm?.cpuCores, body.cpuCores, true);
	if (body.memory) change('Memory', vm?.memory, body.memory, true);
	if (body.instancetype) change('Instance type', vm?.instancetype, body.instancetype, true);
	for (const d of body.addDisks ?? [])
		changes.push({ field: 'Disk', action: 'add', to: `${d.name} (${d.size})`, restart: true });
	for (const d of body.removeDisks ?? [])
		changes.push({ field: 'Disk', action: 'remove', from: d, restart: true });
	return changes;
}

// changeset.stageEdit's contract: incoming fields lay over the staged edit
// (labels by key, lists by name), a field back at git's value drops out, and
// an empty result unstages the VM.
function overlayEdit(staged, incoming, vm) {
	const out = { ...staged, ...incoming };
	const byName = (a = [], b = [], key = (x) => x.name ?? x) => [
		...a.filter((x) => !b.some((y) => key(y) === key(x))),
		...b,
	];
	if (staged.setLabels || incoming.setLabels)
		out.setLabels = { ...staged.setLabels, ...incoming.setLabels };
	for (const k of [
		'removeLabels',
		'addDisks',
		'removeDisks',
		'addNetworks',
		'removeNetworks',
		'addGroups',
		'removeGroups',
		'migrateVolumes',
	])
		if (staged[k] || incoming[k]) out[k] = byName(staged[k], incoming[k]);
	for (const k of [
		'power',
		'cpuCores',
		'memory',
		'instancetype',
		'preference',
		'evictionStrategy',
		'drsExclude',
	])
		if (out[k] !== undefined && vm && String(out[k]) === String(vm[k] ?? '')) delete out[k];
	for (const [k, v] of Object.entries(vm?.labels ?? {}))
		if (out.setLabels?.[k] === v) delete out.setLabels[k];
	for (const k of Object.keys(out))
		if (out[k] === undefined || (typeof out[k] === 'object' && !Object.keys(out[k]).length))
			delete out[k];
	return out;
}

function findVM(ns, name) {
	for (const p of state.scenario.inventory.projects)
		for (const n of p.namespaces) {
			const vm = n.vms.find((v) => v.namespace === ns && v.name === name);
			if (vm) return vm;
		}
	return undefined;
}

// ── the /api surface ───────────────────────────────────────────────────────────

async function handleAPI(req, res, url) {
	const path = url.pathname;
	const s = state.scenario;

	if (path === '/api/healthz') return sendJSON(res, 200, { status: 'ok' });

	if (path === '/api/login' && req.method === 'POST') {
		if (state.loginStatus) {
			const st = state.loginStatus;
			state.loginStatus = 0;
			return sendText(res, st, 'backend unavailable');
		}
		const body = await readBody(req);
		if (!body?.token || body.token === 'bad-token') return sendText(res, 401, 'invalid token');
		res.writeHead(200, {
			'Content-Type': 'application/json',
			'Set-Cookie': `${COOKIE}; Path=/; HttpOnly`,
		});
		return res.end(JSON.stringify(s.user));
	}
	if (path === '/api/auth/methods')
		return sendJSON(res, 200, s.authMethods ?? { sso: false, ssoPending: false });
	if (path === '/api/logout') {
		if (failing('logout')) return sendText(res, 503, 'sign-out failed: fixture failure hook');
		res.writeHead(204, { 'Set-Cookie': 'dv_fixture=; Path=/; Max-Age=0' });
		return res.end();
	}
	if (path === '/api/me') {
		return authed(req) ? sendJSON(res, 200, s.user) : sendText(res, 401, 'no session');
	}
	if (!authed(req)) return sendText(res, 401, 'no session');

	// The create routes and their dry runs (create.mjs).
	const createCtx = { state, sendJSON, sendText, readBody, projectOf, stageInto, draftFor };
	if (await handleCreate(req, res, url, createCtx)) return;

	if (path === '/api/inventory') return sendJSON(res, 200, s.inventory);
	if (path === '/api/options') return sendJSON(res, 200, s.options);
	if (path === '/api/storage/classes') return sendJSON(res, 200, s.storageClasses ?? []);
	if (req.method === 'POST') {
		const repair = await handleRepair(req, res, path, url, s);
		if (repair) return;
	}
	if (path === '/api/networking/gateways') return sendJSON(res, 200, s.gateways);
	if (path === '/api/networking/routes') return sendJSON(res, 200, s.tenantRoutes ?? []);
	if (path === '/api/storage/replication') return sendJSON(res, 200, s.replication);
	if (path === '/api/storage/protection') return sendJSON(res, 200, s.protection ?? []);
	if (path === '/api/networks') return sendJSON(res, 200, s.networks);
	if (path === '/api/policies') return sendJSON(res, 200, s.policies);
	if (path === '/api/tasks') return sendJSON(res, 200, s.tasks);
	if (path === '/api/events') {
		if (failing('events')) return sendText(res, 503, 'events read failed: fixture failure hook');
		return sendJSON(res, 200, s.events);
	}
	if (path === '/api/nodes') {
		if (!clusterCaps(s).hosts) return sendText(res, 403, 'forbidden: not authorized to list nodes');
		return sendJSON(res, 200, s.nodes);
	}
	if (path === '/api/quotas') return sendJSON(res, 200, s.quotas ?? []);
	if (path === '/api/templates') return sendJSON(res, 200, s.templates ?? { templates: [] });
	if (path === '/api/drs') return sendJSON(res, 200, s.rebalancing);
	if (path === '/api/modules') {
		return sendJSON(res, 200, {
			vms: true,
			desktops: false,
			networks: true,
			rebalancing: true,
			routes: false,
			replicatedStorage: false,
			olm: true,
			machineConfig: true,
			...s.modules,
		});
	}
	if (path === '/api/alarms') {
		return s.alarms ? sendJSON(res, 200, s.alarms) : sendText(res, 503, 'alerts feed unavailable');
	}
	if (path === '/api/permissions') {
		return sendJSON(res, 200, { ...s.permissions, namespace: url.searchParams.get('namespace') });
	}
	if (path.startsWith('/api/metrics/') && s.metricsUnavailable) {
		const why = metricsWithout(s.metricsUnavailable);
		if (path === '/api/metrics/cluster') return sendJSON(res, 200, why.cluster);
		if (path === '/api/metrics/hosts') return sendJSON(res, 200, why.hosts);
		if (path === '/api/metrics/capacity') return sendJSON(res, 200, why.capacity);
		if (path === '/api/metrics/scope') return sendJSON(res, 200, why.charts);
		if (path === '/api/metrics/network') return sendJSON(res, 200, why.network);
	}
	if (path.startsWith('/api/metrics/')) {
		if (!s.metrics) return sendText(res, 503, 'metrics not configured');
		if (path === '/api/metrics/cluster') return sendJSON(res, 200, s.metrics.clusterSummary);
		if (path === '/api/metrics/hosts') return sendJSON(res, 200, s.metrics.hostLoad);
		if (path === '/api/metrics/capacity') return sendJSON(res, 200, s.metrics.hostCapacity);
		if (path === '/api/metrics/scope') return sendJSON(res, 200, s.metrics.vmMetrics);
	}

	await handleStorage(req, res, url);
	if (res.headersSent) return;

	if (path === '/api/drafts' && req.method === 'GET') {
		if (state.scenario.draftsDown) return sendText(res, 503, 'drafts unavailable: forge down');
		// The visible projects, and the platform tier for its authors; a lane
		// shows when it holds an entry or warns.
		const names = (state.scenario.inventory?.projects ?? []).map((p) => p.name);
		if (clusterCaps(state.scenario).platform) names.push('platform');
		const drafts = names
			.map((project) => ({ project, draft: draftFor(project) }))
			.filter(({ draft }) => draft.count > 0 || draft.warning);
		return sendJSON(res, 200, { drafts, failed: [] });
	}
	if (path === '/api/draft' && req.method === 'DELETE') {
		state.staged.delete(url.searchParams.get('project'));
		res.writeHead(204);
		return res.end();
	}
	if (path === '/api/draft/propose' && req.method === 'POST') {
		const project = url.searchParams.get('project');
		const body = await readBody(req);
		// A blank title is not an error: the backend fills the default. The error
		// path needs a deterministic trigger, so a literal sentinel title fails.
		if (body?.title === '!fail') return sendText(res, 400, 'propose refused: fixture failure hook');
		if (!draftFor(project).count) return sendText(res, 400, 'nothing staged');
		state.staged.delete(project);
		return sendJSON(res, 200, {
			branch: `dotvirt/proposed/admin/${project}`,
			pushed: true,
			prNumber: 77,
			prURL: `https://forge.example/dotvirt/${project}/pulls/77`,
		});
	}

	let m;
	if (path === '/api/networks/rules') {
		const key = url.searchParams.get('network') ?? '';
		const nss = networkNamespaces(s, key);
		if (!nss) return sendText(res, 404, `not found: network "${key}"`);
		return sendJSON(res, 200, {
			network: key,
			namespaces: nss,
			applies: bindingsFor(s, nss),
			reaches: s.networkReaches?.[key] ?? [],
		});
	}
	if (path === '/api/metrics/network') {
		if (!s.metrics) return sendText(res, 503, 'metrics not configured');
		const key = url.searchParams.get('network') ?? '';
		const limit = Number(url.searchParams.get('limit') ?? 5);
		const net = (s.networks?.networks ?? []).find(
			(x) => x.attachRef === key || (x.scope === 'shared' && x.name === key),
		);
		// A VM is on the network when a NIC names it: by attach ref, or a
		// shared network by its bare name; the pod network takes pod NICs.
		// A pod NIC rides its namespace's primary network when it has one.
		const on = (v, n) => {
			const pod = !n.network || n.network === 'pod';
			if (key === 'Pod network') return pod;
			if (!net) return false;
			if (pod) return net.kind === 'default' && net.namespace === v.namespace;
			return (
				n.network === net.attachRef ||
				(net.scope === 'shared' && n.network.split('/').pop() === net.name)
			);
		};
		const vms = s.inventory.projects
			.flatMap((p) => p.namespaces)
			.flatMap((n) => n.vms)
			.filter((v) => v.phase === 'Running' && (v.networks ?? []).some((n) => on(v, n)));
		return sendJSON(res, 200, {
			network: key,
			window: '1h',
			vms: vms
				.map((v) => ({ namespace: v.namespace, name: v.name, bitsPerSecond: trafficOf(v.name) }))
				.sort((a, b) => b.bitsPerSecond - a.bitsPerSecond)
				.slice(0, limit),
		});
	}
	m = path.match(/^\/api\/objects\/([^/]+)\/([^/]+)\/([^/]+)\/history$/);
	if (m) {
		const project = m[2] === 'cluster' ? 'platform' : (projectOf(m[2])?.name ?? 'platform');
		return sendJSON(res, 200, networkCommits(project, m[3]));
	}
	m = path.match(/^\/api\/projects\/([^/]+)\/release$/);
	if (m && req.method === 'POST') {
		const inv = state.scenario.inventory;
		const p = inv.projects.find((x) => x.name === m[1]);
		if (!p) return sendText(res, 404, 'project not found');
		if (p.repo) return sendText(res, 409, 'project has a repo configured');
		const released = p.namespaces.map((n) => n.namespace);
		inv.projects = inv.projects.filter((x) => x !== p);
		inv.adoptable = [
			...(inv.adoptable ?? []),
			...p.namespaces
				.filter((n) => n.vms.length > 0)
				.map((n) => ({ namespace: n.namespace, vms: n.vms.length })),
		];
		broadcastInventory();
		return sendJSON(res, 200, { released });
	}

	// Each namespace's admin grant gains the project's sync account, staged in
	// the platform draft; refused on the capability the UI gates it by.
	m = path.match(/^\/api\/projects\/([^/]+)\/sync-access$/);
	if (m && req.method === 'POST') {
		const p = state.scenario.inventory.projects.find((x) => x.name === m[1]);
		if (!p) return sendText(res, 404, 'project not found');
		if (!clusterCaps(s).grantAdmin)
			return sendText(res, 403, 'not authorized to create namespaces');
		for (const n of p.namespaces)
			stageInto('platform', {
				kind: 'edit',
				resource: 'rolebinding',
				namespace: n.namespace,
				name: 'admin',
				changes: [{ field: 'Subjects', action: 'add', to: `dotvirt-sync-${p.name}` }],
			});
		return sendJSON(res, 200, draftFor('platform'));
	}

	m = path.match(/^\/api\/projects\/([^/]+)\/(history(\/|$)|revert$|proposals\/(\d+)$)/);
	if (m && m[1] === 'platform' && !m[4] && !clusterCaps(s).platformRead)
		return sendText(res, 403, 'forbidden: not authorized to read the platform repo');
	// A tenant repo's reads answer as the server's gateRepo does: the whole
	// repo on ProjectCaps.repoRead, one namespace on its own repoRead. The
	// caller's own PR opens either way (ownProposal).
	const repoProject = m && s.inventory.projects.find((p) => p.name === m[1]);
	const ownPR = (n) =>
		(s.inventory.proposals ?? []).some((p) => p.project === m[1] && p.prNumber === n && p.mine);
	if (repoProject && !(m[4] && ownPR(Number(m[4])))) {
		const ns = path.endsWith('/history') ? url.searchParams.get('namespace') : null;
		const c = namespaceCaps(s, repoProject, ns ?? repoProject.namespaces[0]?.namespace);
		if (!(ns ? c.repoRead : c.project.repoRead))
			return sendText(res, 403, `forbidden: this needs every namespace of ${m[1]} in your view`);
	}
	m = path.match(/^\/api\/projects\/([^/]+)\/history$/);
	if (m) {
		// ?namespace= narrows to the commits under that directory: the merge
		// touched web-prod only, the import touched everything.
		const ns = url.searchParams.get('namespace');
		if (ns && ns !== 'web-prod') return sendJSON(res, 200, [initialCommit()]);
		return sendJSON(res, 200, [mergeCommit(m[1]), initialCommit()]);
	}
	// One past change under review: the same items a staged draft renders.
	m = path.match(/^\/api\/projects\/([^/]+)\/history\/([0-9a-f]{40})$/);
	if (m) {
		if (m[2] !== MERGE_HASH) return sendText(res, 404, 'not found: commit');
		return sendJSON(res, 200, {
			commit: mergeCommit(m[1]),
			items: [
				{
					kind: 'edit',
					namespace: 'web-prod',
					name: 'web-2',
					changes: [{ field: 'Disk', action: 'add', to: 'data (50Gi)' }],
					yaml: WEB2_BASE + '  - name: data\n    size: 50Gi\n',
					baseYAML: WEB2_BASE,
				},
			],
		});
	}
	// One open PR under review: the diff its head carries, as draft items.
	m = path.match(/^\/api\/projects\/([^/]+)\/proposals\/(\d+)$/);
	if (m) {
		const pr = (s.inventory.proposals ?? []).find(
			(p) => p.project === m[1] && p.prNumber === Number(m[2]),
		);
		if (!pr) return sendText(res, 404, `not found: pull request #${m[2]}`);
		const denied = s.proposalDenied?.[`${m[1]}#${m[2]}`];
		if (denied) return sendText(res, 403, denied);
		const items = {
			41: {
				kind: 'edit',
				namespace: 'db-prod',
				name: 'db-1',
				changes: [{ field: 'Memory', action: 'change', from: '8Gi', to: '12Gi' }],
				yaml: 'kind: VirtualMachine\nmetadata:\n  name: db-1\nspec:\n  memory: 12Gi\n',
				baseYAML: 'kind: VirtualMachine\nmetadata:\n  name: db-1\nspec:\n  memory: 8Gi\n',
			},
			7: {
				kind: 'create',
				namespace: 'cluster',
				name: 'dc-vlan-120',
				resource: 'network',
				changes: [{ field: 'VLAN', action: 'add', to: '120' }],
				yaml: 'kind: ClusterUserDefinedNetwork\nmetadata:\n  name: dc-vlan-120\n',
			},
			42: {
				kind: 'edit',
				namespace: 'web-prod',
				name: 'web-1',
				changes: [{ field: 'Balancer', action: 'change', from: 'Automatic', to: 'Excluded' }],
				yaml: 'kind: VirtualMachine\nmetadata:\n  name: web-1\n  annotations:\n    descheduler.alpha.kubernetes.io/prefer-no-eviction: "true"\n',
				baseYAML: 'kind: VirtualMachine\nmetadata:\n  name: web-1\n',
			},
		}[pr.prNumber];
		return sendJSON(res, 200, { proposal: pr, items: items ? [items] : [] });
	}
	m = path.match(/^\/api\/projects\/([^/]+)\/revert$/);
	if (m && req.method === 'POST') {
		const body = await readBody(req);
		if (body?.hash !== MERGE_HASH)
			return sendText(res, 400, 'commit hash must be the full 40-character hash');
		return sendJSON(res, 200, {
			branch: `dotvirt/proposed/revert/admin/${m[1]}-${MERGE_HASH.slice(0, 8)}`,
			pushed: true,
			prNumber: 78,
			prURL: `https://forge.example/dotvirt/${m[1]}/pulls/78`,
		});
	}

	m = path.match(/^\/api\/vms\/([^/]+)\/([^/]+)\/edit\/preview$/);
	if (m && req.method === 'POST') {
		const body = await readBody(req);
		return sendJSON(res, 200, editChanges(body ?? {}, findVM(m[1], m[2])));
	}

	m = path.match(/^\/api\/vms\/([^/]+)\/([^/]+)(?:\/([a-z]+))?$/);
	if (m) {
		const [, ns, name, sub] = m;
		if (req.method === 'POST') {
			if (sub === 'edit') {
				const body = await readBody(req);
				if (!body?.sourceFile) return sendText(res, 400, 'sourceFile is required');
				// The server lists every bad field, one per line; 99 vCPU is the
				// deterministic trigger.
				if (body.cpuCores === 99)
					return sendText(
						res,
						400,
						'invalid: CPU count 99 is more than any host offers\ninvalid: memory "99Ti" is not available\n',
					);
				const { sourceFile: _, ...incoming } = body;
				const vm = findVM(ns, name);
				const proj = projectOf(ns);
				const prev =
					proj && state.staged.get(proj.name)?.find((i) => i.namespace === ns && i.name === name);
				if (prev && (prev.kind !== 'edit' || !prev.edit))
					return sendText(res, 409, `${ns}/${name} has a staged ${prev.kind} waiting`);
				const edit = overlayEdit(prev?.edit ?? {}, incoming, vm);
				if (proj && !Object.keys(edit).length) {
					const rest = (state.staged.get(proj.name) ?? []).filter((i) => i !== prev);
					state.staged.set(proj.name, rest);
					return sendJSON(res, 200, draftFor(proj.name));
				}
				const changes = editChanges(edit, vm);
				return stage(res, ns, name, {
					kind: 'edit',
					changes: changes.length ? changes : [{ field: 'manifest', action: 'change' }],
					edit,
				});
			}
			if (sub === 'delete') return stage(res, ns, name, { kind: 'delete', changes: [] });
			if (sub === 'restore') {
				const body = await readBody(req);
				if (body?.hash !== MERGE_HASH && body?.hash !== INITIAL_HASH)
					return sendText(res, 400, 'commit hash must be the full 40-character hash');
				return stage(res, ns, name, {
					kind: 'edit',
					changes: [
						{ field: 'Restore', action: 'change', to: `version ${body.hash.slice(0, 8)}` },
						{ field: 'Disk', action: 'remove', from: 'data (50Gi)' },
					],
				});
			}
			if (sub === 'adopt') {
				return stage(res, ns, name, {
					kind: 'create',
					changes: [{ field: 'manifest', action: 'add', to: 'captured from cluster' }],
				});
			}
			// Imperative runtime verbs: restart / pause / unpause / migrate ...
			res.writeHead(204);
			return res.end();
		}
		if (sub === 'drift') {
			return sendJSON(res, 200, s.vmDrift?.[`${ns}/${name}`] ?? { drift: false, changes: [] });
		}
		if (sub === 'history') {
			return sendJSON(
				res,
				200,
				ns === 'web-prod' && name === 'web-2' ? [mergeCommit('team-web'), initialCommit()] : [],
			);
		}
		if (sub === 'events') {
			if (failing('events')) return sendText(res, 503, 'events read failed: fixture failure hook');
			return sendJSON(
				res,
				200,
				(s.events ?? []).filter((e) => e.name === name),
			);
		}
		if (s.metricsUnavailable && (sub === 'usage' || sub === 'metrics')) {
			const why = metricsWithout(s.metricsUnavailable);
			return sendJSON(res, 200, sub === 'usage' ? why.usage : why.charts);
		}
		if (sub === 'usage') return sendJSON(res, 200, s.metrics?.vmUsage ?? {});
		if (sub === 'metrics') {
			if (!s.metrics) return sendText(res, 503, 'metrics not configured');
			return sendJSON(res, 200, s.metrics.vmMetrics);
		}
		if (sub === 'clones') return sendJSON(res, 200, []);
		if (sub === 'policy') return sendJSON(res, 200, effectiveFor(s, ns, name));
	}

	m = path.match(/^\/api\/namespaces\/([^/]+)\/policy$/);
	if (m)
		return sendJSON(res, 200, { ...effectiveFor(s, m[1], ''), vm: undefined, labels: undefined });

	// --- hosts: the maintenance verbs ---
	// Cordon and maintenance flip the scenario's node, so the next read (the
	// host's own, the list) says what changed; evacuate counts the running VMs
	// placed there as requested.
	m = path.match(/^\/api\/nodes\/([^/]+)\/(cordon|maintenance|evacuate)$/);
	if (m && req.method === 'POST') {
		const n = s.nodes.find((x) => x.name === m[1]);
		if (!n) return sendText(res, 404, `node ${m[1]} not found`);
		if (m[2] === 'evacuate') {
			const here = s.inventory.projects
				.flatMap((p) => p.namespaces.flatMap((ns) => ns.vms))
				.filter((v) => v.nodeName === n.name && v.phase === 'Running');
			return sendJSON(res, 200, { requested: here.length, failures: [] });
		}
		const body = await readBody(req);
		if (m[2] === 'cordon') n.unschedulable = !!body?.unschedulable;
		else {
			n.maintenance = !!body?.enter;
			n.unschedulable = !!body?.enter;
		}
		res.writeHead(204);
		return res.end();
	}
	// --- end hosts ---

	m = path.match(/^\/api\/nodes\/([^/]+)$/);
	if (m) {
		const n = s.nodes.find((x) => x.name === m[1]);
		return sendJSON(res, 200, {
			name: m[1],
			unschedulable: !!n?.unschedulable,
			maintenance: !!n?.maintenance,
			canCordon: true,
		});
	}

	return sendText(res, 503, `fixture: no handler for ${req.method} ${path}`);
}

// ── storage (S3): snapshots, claims, uploads ───────────────────────────────────
// Answers the storage routes; a request it leaves unanswered goes on to the
// rest of the API. A scenario's null listing answers 503, like an unreachable
// cluster read.

// The replication repairs write the scenario's clone, so a switch forgets them.
function evictPlan(s, node) {
	const moving = [];
	const skipped = [];
	for (const v of s.replication.volumes) {
		if (!v.replicas.some((r) => r.node === node)) continue;
		let reason = '';
		if (v.copies <= 1)
			reason = 'One copy: it cannot move. Recreate it on replicated storage first.';
		else if (v.splitBrain) reason = 'Its copies disagree: resolve that first.';
		else if (v.replicas.some((r) => r.state === 'catching-up'))
			reason = 'A copy is still catching up.';
		if (reason) skipped.push({ volume: v.name, reason });
		else moving.push(v.name);
	}
	return { moving, skipped };
}

async function handleRepair(req, res, path, url, s) {
	let m = path.match(/^\/api\/storage\/replication\/volumes\/([^/]+)\/resolve$/);
	if (m) {
		const body = await readBody(req);
		const volume = s.replication.volumes.find((v) => v.name === m[1]);
		if (volume) volume.resolving = body.survivor;
		sendText(res, 204, '');
		return true;
	}
	m = path.match(/^\/api\/storage\/replication\/hosts\/([^/]+)\/evict$/);
	if (m) {
		const plan = evictPlan(s, m[1]);
		if (url.searchParams.get('dryRun') !== 'true')
			for (const v of s.replication.volumes) if (plan.moving.includes(v.name)) v.evicting = [m[1]];
		sendJSON(res, 200, plan);
		return true;
	}
	return false;
}

const newestFirst = (a, b) => (b.created ?? '').localeCompare(a.created ?? '');

// The caller's verdicts (GET /api/capabilities): everything allowed, unless
// the scenario's `capabilities` narrows a namespace (by name, or `default`).
// A namespace the frame denies authoring denies every staged change; one the
// frame carries no verdict for answers 503, a review that did not complete.
const allowAll = { create: true, update: true, delete: true };
const denyAll = { create: false, update: false, delete: false };
function namespaceCaps(s, project, ns) {
	const author = project.namespaces.find((n) => n.namespace === ns)?.canAuthor !== false;
	const objects = author ? allowAll : denyAll;
	const override = s.capabilities?.[ns] ?? s.capabilities?.default;
	return {
		namespace: ns,
		vm: objects,
		resync: true,
		restart: true,
		migrate: true,
		pause: true,
		unpause: true,
		console: true,
		snapshotTake: true,
		snapshotRestore: true,
		snapshotDelete: true,
		clone: true,
		dataVolumeCreate: true,
		bootImageCreate: true,
		bootImageImport: true,
		bootImageDelete: true,
		network: objects,
		networkAttachment: objects,
		networkPolicy: objects,
		egressFirewall: objects,
		repoRead: true,
		...override,
		project: {
			name: project.name,
			repoRead: true,
			templateEdit: author,
			newNamespace: true,
			adopt: author,
			grantAdmin: author,
			release: !project.repo,
			syncAccess: author && !!project.repo,
			...override?.project,
		},
	};
}
// A metrics view the server could not fill: 200 with the reason and no
// numbers, as the server answers since metrics tenancy (alarms keep their 503).
function metricsWithout(reason) {
	const none = { used: 0, total: 0 };
	return {
		cluster: {
			updated: 0,
			cpu: none,
			memory: none,
			storage: none,
			topCpu: [],
			topMemory: [],
			unavailable: reason,
		},
		hosts: { updated: 0, workers: 0, mean: 0, nodes: [], unavailable: reason },
		capacity: { updated: 0, nodes: [], unavailable: reason },
		charts: { range: '1h', charts: [], unavailable: reason },
		usage: {
			updated: 0,
			cpu: { used: 0 },
			memory: { used: 0 },
			storage: { used: 0 },
			unavailable: reason,
		},
		network: { network: '', window: '1h', vms: [], unavailable: reason },
	};
}

// The cluster verdicts follow the scenario's /api/networks signals, which the
// server derives from the same checks, so a scenario narrows both at once.
function clusterCaps(s) {
	const n = s.networks ?? {};
	const c = n.caps ?? {};
	const objects = (on) => (on ? allowAll : denyAll);
	return {
		platform: !!n.canManage,
		platformRead: !!c.platform,
		network: objects(n.canManage),
		uplink: objects(c.uplink),
		egressIP: objects(c.egressIP),
		externalRoute: objects(c.externalRoute),
		adminNetworkPolicy: objects(c.adminNetworkPolicy),
		baselineAdminNetworkPolicy: objects(c.adminNetworkPolicy),
		namespaceCreate: !!c.namespace,
		vmNetwork: !!c.namespace,
		grantAdmin: !!c.namespace,
		sharedLibrary: !!n.canManage,
		drsEnable: !!s.rebalancing?.canManage,
		drsPSI: !!s.rebalancing?.canPSI,
		drsDisable: !!s.rebalancing?.canManage,
		oauthClient: !!n.canManage,
		imageUpload: true,
		hosts: true,
		...s.capabilities?.cluster,
	};
}

async function handleStorage(req, res, url) {
	const path = url.pathname;
	const s = state.scenario;
	const listing = (v) =>
		v === null ? sendText(res, 503, 'cluster read failed') : sendJSON(res, 200, v ?? []);
	if (path === '/api/storage/snapshots') return listing(s.storageSnapshots);
	if (path === '/api/capabilities') {
		const ns = url.searchParams.get('namespace');
		if (!ns) return sendJSON(res, 200, { cluster: clusterCaps(s) });
		const p = projectOf(ns);
		if (!p) return sendText(res, 404, 'namespace not found in any visible project');
		if (p.repo && !('canAuthor' in p.namespaces.find((n) => n.namespace === ns)))
			return sendText(res, 503, 'access review failed');
		return sendJSON(res, 200, { namespace: namespaceCaps(s, p, ns) });
	}
	if (path === '/api/storage/claims') return listing(s.storageClaims);

	let m = path.match(/^\/api\/vms\/([^/]+)\/([^/]+)\/snapshots$/);
	if (m) {
		const [, ns, vmName] = m;
		if (req.method === 'GET')
			return sendJSON(
				res,
				200,
				(s.storageSnapshots ?? [])
					.filter((x) => x.namespace === ns && x.vm === vmName)
					.sort(newestFirst),
			);
		if (req.method === 'POST') {
			const body = await readBody(req);
			const name =
				body?.name || `${vmName}-${new Date().toISOString().replace(/\D/g, '').slice(0, 14)}`;
			const vmObj = findVM(ns, vmName);
			s.storageSnapshots = [
				{
					namespace: ns,
					vm: vmName,
					name,
					created: new Date().toISOString(),
					phase: 'Succeeded',
					readyToUse: true,
					volumes: (vmObj?.disks ?? [])
						.filter((d) => d.type === 'dataVolume' || d.type === 'pvc')
						.map((d) => d.name),
				},
				...(s.storageSnapshots ?? []),
			];
			return sendJSON(res, 200, { name });
		}
	}
	m = path.match(/^\/api\/vms\/([^/]+)\/([^/]+)\/snapshots\/([^/]+)(\/restore)?$/);
	if (m) {
		const [, ns, , snapName, restore] = m;
		if (restore && req.method === 'POST') {
			res.writeHead(204);
			return res.end();
		}
		if (!restore && req.method === 'DELETE') {
			s.storageSnapshots = (s.storageSnapshots ?? []).filter(
				(x) => !(x.namespace === ns && x.name === snapName),
			);
			res.writeHead(204);
			return res.end();
		}
	}

	if (path === '/api/uploads' && req.method === 'POST') {
		const body = await readBody(req);
		if (!body?.namespace || !body?.name || !body?.size)
			return sendText(res, 400, 'namespace, name and size are required');
		if (!projectOf(body.namespace))
			return sendText(res, 404, 'namespace not found in any visible project');
		state.uploads.set(`${body.namespace}/${body.name}`, 'UploadReady');
		// A boot image joins the catalog New VM reads.
		if (body.bootImage)
			s.options = {
				...s.options,
				osImages: [
					...s.options.osImages,
					{ name: body.name, namespace: body.namespace, ready: true, removable: true },
				],
			};
		return sendJSON(res, 200, {
			namespace: body.namespace,
			name: body.name,
			...(body.bootImage ? { image: `${body.namespace}/${body.name}` } : {}),
		});
	}
	// ── imports: a boot image the cluster downloads ──
	// Answers as the server does: the same checks before anything is made, a
	// DataSource naming the source without its query string, then CDI's
	// import phases one status poll at a time. A source naming "unreachable"
	// never lands: the importer fails and retries, as CDI does.
	if (path === '/api/imports' && req.method === 'POST') {
		const body = await readBody(req);
		if (!body?.namespace || !body?.name || !body?.size)
			return sendText(res, 400, 'namespace, name and size are required');
		const src = String(body.source ?? '');
		const bad =
			!src || /\s/.test(src)
				? 'is required and may not contain spaces'
				: body.kind === 'url'
					? !/^https?:\/\/[^/?#@]+/i.test(src) && 'must be an http or https URL'
					: body.kind === 'registry'
						? /:\/\//.test(src.replace(/^docker:\/\//i, '')) && 'must be an image reference'
						: 'kind must be "url" or "registry"';
		if (bad) return sendText(res, 400, `invalid: the source ${bad}`);
		if (!projectOf(body.namespace))
			return sendText(res, 404, 'namespace not found in any visible project');
		const key = `${body.namespace}/${body.name}`;
		if (state.uploads.has(key))
			return sendText(res, 409, `datavolumes "${body.name}" already exists`);
		const unreachable = src.includes('unreachable');
		// A source naming "corrupt" downloads, then fails for good.
		const corrupt = src.includes('corrupt');
		state.uploads.set(key, {
			phases: unreachable
				? ['ImportScheduled', 'ImportInProgress']
				: corrupt
					? ['ImportScheduled', 'ImportInProgress', 'Failed']
					: ['ImportScheduled', 'ImportInProgress', 'ImportInProgress', 'Succeeded'],
			at: 0,
			error: unreachable
				? `Unable to connect to http data source: Get "${src.split('?')[0]}": dial tcp: lookup unreachable.example: no such host`
				: corrupt
					? 'Unable to process data: qemu-img: Could not open the image: not in a known disk format'
					: '',
		});
		const shown = src.replace(/^docker:\/\//i, '').split(/[?#]/)[0];
		s.options = {
			...s.options,
			osImages: [
				...s.options.osImages,
				{
					name: body.name,
					namespace: body.namespace,
					ready: !unreachable && !corrupt,
					removable: true,
					import: { kind: body.kind, source: shown },
				},
			],
		};
		return sendJSON(res, 200, {
			namespace: body.namespace,
			name: body.name,
			image: `${body.namespace}/${body.name}`,
		});
	}

	// ── boot image delete: one that never finished ──
	// Refuses as the server does: a caller without the right, one it cannot
	// find, one dotvirt did not add, a ready one. Else the catalog and the
	// status read drop it.
	m = path.match(/^\/api\/images\/([^/]+)\/([^/]+)$/);
	if (m && req.method === 'DELETE') {
		const [ns, name] = [decodeURIComponent(m[1]), decodeURIComponent(m[2])];
		const p = projectOf(ns);
		if (!p) return sendText(res, 404, 'namespace not found in any visible project');
		if (!namespaceCaps(s, p, ns).bootImageDelete)
			return sendText(res, 403, `forbidden: you may not delete boot images in ${ns}`);
		const img = s.options.osImages.find((i) => i.namespace === ns && i.name === name);
		if (!img) return sendText(res, 404, `datasources.cdi.kubevirt.io "${name}" not found`);
		if (!img.removable)
			return sendText(
				res,
				409,
				`conflict: ${name} is not a boot image dotvirt added (it points at another disk); nothing was removed`,
			);
		if (img.ready)
			return sendText(
				res,
				409,
				`conflict: ${name} is ready, and only an unfinished boot image is deleted here; nothing was removed`,
			);
		const key = `${ns}/${name}`;
		s.options = { ...s.options, osImages: s.options.osImages.filter((i) => i !== img) };
		state.uploads.delete(key);
		if (s.imageStatus?.[key]) s.imageStatus = { ...s.imageStatus, [key]: undefined };
		return sendJSON(res, 200, {});
	}

	m = path.match(/^\/api\/uploads\/([^/]+)\/([^/]+)(\/token)?$/);
	if (m) {
		const key = `${m[1]}/${m[2]}`;
		const entry = state.uploads.get(key);
		// A scenario's own unfinished boot images answer one fixed status.
		if (!entry && !m[3] && s.imageStatus?.[key]) return sendJSON(res, 200, s.imageStatus[key]);
		if (!entry) return sendText(res, 404, 'not found: upload');
		if (typeof entry === 'object' && !m[3]) {
			const phase = entry.phases[Math.min(entry.at++, entry.phases.length - 1)];
			return sendJSON(res, 200, {
				phase,
				ready: false,
				progress: phase === 'ImportInProgress' ? '42.0%' : phase === 'Succeeded' ? '100.0%' : 'N/A',
				...(entry.error && phase !== 'ImportScheduled' ? { error: entry.error } : {}),
			});
		}
		const phase = entry;
		if (m[3] && req.method === 'POST')
			return sendJSON(res, 200, {
				token: 'fixture-upload-token',
				uploadUrl: `http://localhost:${port}/__fixture/upload/${encodeURIComponent(key)}`,
			});
		if (!m[3])
			return sendJSON(res, 200, { phase, ready: phase === 'UploadReady', progress: '100.0%' });
	}
	return undefined;
}

// ── control plane ──────────────────────────────────────────────────────────────

function handleControl(req, res, url) {
	const path = url.pathname;
	if (path === '/__fixture/scenario' && req.method === 'GET') {
		return sendJSON(res, 200, { scenario: state.scenario.name });
	}
	let m = path.match(/^\/__fixture\/scenario\/([a-z]+)$/);
	if (m && req.method === 'POST') {
		const next = scenarios[m[1]];
		if (!next) return sendText(res, 404, `no scenario ${m[1]}`);
		// Deep clone: endpoints that mutate scenario state (release) must never
		// pollute the shared definitions across tests.
		state.scenario = structuredClone(next);
		state.staged.clear();
		state.fail.clear();
		state.uploads.clear();
		broadcastInventory();
		return sendJSON(res, 200, { scenario: next.name });
	}
	if (path === '/__fixture/ws-drop' && req.method === 'POST') {
		for (const ws of state.sockets) ws.terminate();
		return sendJSON(res, 200, { dropped: true });
	}
	m = path.match(/^\/__fixture\/ws-refuse\/(\d+)$/);
	if (m && req.method === 'POST') {
		state.wsRefuse = Number(m[1]);
		return sendJSON(res, 200, { refuse: state.wsRefuse });
	}
	// The upload proxy the browser streams an image to: the bytes are dropped,
	// the upload then reads as imported.
	m = path.match(/^\/__fixture\/upload\/(.+)$/);
	if (m && req.method === 'POST') {
		const key = decodeURIComponent(m[1]);
		req.resume();
		req.on('end', () => {
			if (state.uploads.has(key)) state.uploads.set(key, 'Succeeded');
			res.writeHead(200);
			res.end();
		});
		return;
	}
	m = path.match(/^\/__fixture\/fail\/([a-z]+)\/(\d+)$/);
	if (m && req.method === 'POST') {
		state.fail.set(m[1], Number(m[2]));
		return sendJSON(res, 200, { plane: m[1], fail: Number(m[2]) });
	}
	m = path.match(/^\/__fixture\/login-status\/(\d+)$/);
	if (m && req.method === 'POST') {
		state.loginStatus = Number(m[1]);
		return sendJSON(res, 200, { loginStatus: state.loginStatus });
	}
	return sendText(res, 404, 'unknown fixture control');
}

// ── static SPA + dispatch ──────────────────────────────────────────────────────

function serveStatic(req, res, url) {
	const clean = normalize(url.pathname).replace(/^(\.\.[/\\])+/, '');
	let file = join(buildDir, clean);
	if (!existsSync(file) || statSync(file).isDirectory()) file = join(buildDir, 'index.html');
	res.writeHead(200, { 'Content-Type': MIME[extname(file)] ?? 'application/octet-stream' });
	createReadStream(file).pipe(res);
}

const server = createServer((req, res) => {
	const url = new URL(req.url, `http://localhost:${port}`);
	if (url.pathname.startsWith('/__fixture/')) return handleControl(req, res, url);
	if (url.pathname.startsWith('/api/')) {
		handleAPI(req, res, url).catch((e) => {
			console.error(e);
			sendText(res, 500, 'fixture: the handler failed, see its log');
		});
		return;
	}
	serveStatic(req, res, url);
});

const wss = new WebSocketServer({ noServer: true });
server.on('upgrade', (req, socket, head) => {
	const url = new URL(req.url, `http://localhost:${port}`);
	if (url.pathname !== '/api/inventory/stream') return socket.destroy();
	if (state.wsRefuse > 0) {
		// Simulate a backend deploy/blip: the handshake dies before the socket
		// ever opens, the client's session-probe path decides reconnect vs logout.
		state.wsRefuse--;
		return socket.destroy();
	}
	if (!(req.headers.cookie ?? '').includes(COOKIE)) {
		socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n');
		return socket.destroy();
	}
	wss.handleUpgrade(req, socket, head, (ws) => {
		state.sockets.add(ws);
		ws.on('close', () => state.sockets.delete(ws));
		ws.send(JSON.stringify(state.scenario.inventory));
	});
});

server.listen(port, () =>
	console.log(`fixture: ${state.scenario.name} on http://localhost:${port}`),
);
