// Hermetic fixture backend: serves the built SPA plus a scripted /api that
// replays scenario states (scenarios.mjs), including the WS inventory stream.
// The UI runs against it exactly as against the Go backend, with no cluster,
// forge, or Argo — so CI can hold the app in corner-case states on demand.
//
//   node e2e/fixture/server.mjs        (expects ../build from `npm run build`)
//
// Control plane (no auth, test-only):
//   POST /__fixture/scenario/{name}    switch state; resets session drafts
//   POST /__fixture/ws-drop            close every live inventory socket
//   POST /__fixture/ws-refuse/{n}      destroy the next n WS handshakes pre-open
//   POST /__fixture/login-status/{s}   next POST /api/login answers status s
//   GET  /__fixture/scenario           current scenario name

import { createReadStream, existsSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { WebSocketServer } from 'ws';
import { scenarios } from './scenarios.mjs';

const buildDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'build');
if (!existsSync(join(buildDir, 'index.html'))) {
	console.error(`fixture: no SPA build at ${buildDir} — run \`npm run build\` first`);
	process.exit(1);
}

const port = Number(process.env.FIXTURE_PORT ?? 4173);
const COOKIE = 'dv_fixture=1';

const state = {
	scenario: scenarios[process.env.FIXTURE_SCENARIO ?? 'base'],
	// Per-project items staged through this session's POSTs, merged over the
	// scenario's own drafts so staging flows work end to end.
	staged: new Map(),
	loginStatus: 0, // one-shot override for POST /api/login
	wsRefuse: 0, // destroy the next n WS handshakes before the socket opens
	sockets: new Set(),
};

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
	const items = state.staged.get(p.name) ?? [];
	items.push({ namespace, name, ...item });
	state.staged.set(p.name, items);
	sendJSON(res, 200, draftFor(p.name));
}

// Human-readable change rows from a VMEdit body, the way the backend's semantic
// diff would spell them — enough for the Changes panel to render real content.
function editChanges(body) {
	const changes = [];
	if (body.power) changes.push({ field: 'power', action: 'change', to: body.power });
	if (body.cpuCores)
		changes.push({ field: 'cpuCores', action: 'change', to: String(body.cpuCores), restart: true });
	if (body.memory)
		changes.push({ field: 'memory', action: 'change', to: body.memory, restart: true });
	if (body.instancetype)
		changes.push({ field: 'instancetype', action: 'change', to: body.instancetype });
	for (const d of body.addDisks ?? [])
		changes.push({ field: `disk ${d.name}`, action: 'add', to: d.size });
	for (const d of body.removeDisks ?? []) changes.push({ field: `disk ${d}`, action: 'remove' });
	if (!changes.length) changes.push({ field: 'manifest', action: 'change' });
	return changes;
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
		res.writeHead(200, { 'Set-Cookie': 'dv_fixture=; Path=/; Max-Age=0' });
		return res.end();
	}
	if (path === '/api/me') {
		return authed(req) ? sendJSON(res, 200, s.user) : sendText(res, 401, 'no session');
	}
	if (!authed(req)) return sendText(res, 401, 'no session');

	if (path === '/api/inventory') return sendJSON(res, 200, s.inventory);
	if (path === '/api/options') return sendJSON(res, 200, s.options);
	if (path === '/api/networks') return sendJSON(res, 200, s.networks);
	if (path === '/api/policies') return sendJSON(res, 200, s.policies);
	if (path === '/api/tasks') return sendJSON(res, 200, s.tasks);
	if (path === '/api/events') return sendJSON(res, 200, s.events);
	if (path === '/api/nodes') return sendJSON(res, 200, s.nodes);
	if (path === '/api/quotas') return sendJSON(res, 200, s.quotas ?? []);
	if (path === '/api/templates') return sendJSON(res, 200, s.templates ?? { templates: [] });
	if (path === '/api/drs') return sendJSON(res, 200, s.drs);
	if (path === '/api/alarms') {
		return s.alarms ? sendJSON(res, 200, s.alarms) : sendText(res, 503, 'alerts feed unavailable');
	}
	if (path === '/api/permissions') {
		return sendJSON(res, 200, { ...s.permissions, namespace: url.searchParams.get('namespace') });
	}
	if (path.startsWith('/api/metrics/')) {
		if (!s.metrics) return sendText(res, 503, 'metrics not configured');
		if (path === '/api/metrics/cluster') return sendJSON(res, 200, s.metrics.clusterSummary);
		if (path === '/api/metrics/hosts') return sendJSON(res, 200, s.metrics.hostLoad);
		if (path === '/api/metrics/capacity') return sendJSON(res, 200, s.metrics.hostCapacity);
		if (path === '/api/metrics/scope') return sendJSON(res, 200, s.metrics.vmMetrics);
	}

	if (path === '/api/draft' && req.method === 'GET') {
		return sendJSON(res, 200, draftFor(url.searchParams.get('project')));
	}
	if (path === '/api/draft' && req.method === 'DELETE') {
		state.staged.delete(url.searchParams.get('project'));
		res.writeHead(204);
		return res.end();
	}
	if (path === '/api/draft/propose' && req.method === 'POST') {
		const project = url.searchParams.get('project');
		const body = await readBody(req);
		if (!body?.title) return sendText(res, 400, 'title is required');
		state.staged.delete(project);
		return sendJSON(res, 200, {
			branch: `dotvirt/proposed/admin/${project}`,
			pushed: true,
			prNumber: 77,
			prURL: `https://forge.example/dotvirt/${project}/pulls/77`,
		});
	}

	let m = path.match(/^\/api\/projects\/([^/]+)\/history$/);
	if (m) {
		return sendJSON(res, 200, [
			{
				hash: 'ab12cd34ef',
				shortHash: 'ab12cd3',
				message: 'web-2: add data disk',
				author: 'admin',
				when: new Date(Date.now() - 6e6).toISOString(),
			},
			{
				hash: '99fe210aaa',
				shortHash: '99fe210',
				message: 'initial import',
				author: 'admin',
				when: new Date(Date.now() - 9e7).toISOString(),
				merge: true,
			},
		]);
	}

	if (path === '/api/vms' && req.method === 'POST') {
		const body = await readBody(req);
		if (!body?.namespace) return sendText(res, 400, 'spec namespace is required');
		return stage(res, body.namespace, body.name, {
			kind: 'create',
			changes: [{ field: 'manifest', action: 'add', to: body.instancetype ?? 'custom' }],
		});
	}

	m = path.match(/^\/api\/vms\/([^/]+)\/([^/]+)(?:\/([a-z]+))?$/);
	if (m) {
		const [, ns, name, sub] = m;
		if (req.method === 'POST') {
			if (sub === 'edit') {
				const body = await readBody(req);
				if (!body?.sourceFile) return sendText(res, 400, 'sourceFile is required');
				return stage(res, ns, name, { kind: 'edit', changes: editChanges(body) });
			}
			if (sub === 'delete') return stage(res, ns, name, { kind: 'delete', changes: [] });
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
		if (sub === 'events') {
			return sendJSON(
				res,
				200,
				(s.events ?? []).filter((e) => e.name === name),
			);
		}
		if (sub === 'usage') return sendJSON(res, 200, s.metrics?.vmUsage ?? {});
		if (sub === 'metrics') {
			if (!s.metrics) return sendText(res, 503, 'metrics not configured');
			return sendJSON(res, 200, s.metrics.vmMetrics);
		}
		if (sub === 'snapshots') return sendJSON(res, 200, []);
		if (sub === 'clones') return sendJSON(res, 200, []);
		if (sub === 'policy') {
			return sendJSON(res, 200, {
				namespace: ns,
				vm: name,
				labels: { app: name },
				eastWest: [],
				gateway: [],
			});
		}
	}

	m = path.match(/^\/api\/namespaces\/([^/]+)\/policy$/);
	if (m) return sendJSON(res, 200, { namespace: m[1], eastWest: [], gateway: [] });

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
		state.scenario = next;
		state.staged.clear();
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
		handleAPI(req, res, url).catch((e) => sendText(res, 500, String(e)));
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
