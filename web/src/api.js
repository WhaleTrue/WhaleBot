let csrf = '';
export function setCSRF(value) { csrf = value || ''; }
export async function request(path, method = 'GET', input, signal) {
  const response = await fetch(path, { method, credentials: 'same-origin', cache: 'no-store', signal, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: input === undefined ? undefined : JSON.stringify(input) });
  if (!response.ok) {
    const text = await response.text();
    let message = text;
    try { const data = JSON.parse(text); message = data.error?.message || data.error || data.message || text; } catch { /* Non-JSON provider errors remain visible. */ }
    throw new Error(`${response.status}: ${message}`);
  }
  return response;
}
export async function json(path, method = 'GET', input) { return (await request(path, method, input)).json(); }
export function nodeID(node) {
  const identity = node.identity;
  return [identity.namespace, identity.principal, identity.name, identity.instance_id].map(encodeURIComponent).join('/');
}
export async function call(provider, operation, input = {}, options = {}) {
  const { signal, ...fields } = options;
  return request('/api/call', 'POST', { capability: provider.capability.id, node: nodeID(provider.node), operation: operation.name, input, ...fields }, signal);
}
export async function readResult(response) {
  if (response.headers.get('content-type')?.includes('json')) return response.json();
  const blob = await response.blob();
  return { download: blob, contentType: blob.type, filename: response.headers.get('content-disposition')?.match(/filename="?([^";]+)/)?.[1] || 'artifact.bin' };
}
export function download(blob, name) { const url = URL.createObjectURL(blob); const a = document.createElement('a'); a.href = url; a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
export async function consumeSSE(response, onEvent) {
  const reader = response.body.getReader(); const decoder = new TextDecoder(); let pending = '';
  try {
    while (true) {
      const { value, done } = await reader.read(); pending = (pending + decoder.decode(value, { stream: !done })).replace(/\r\n/g, '\n');
      let boundary;
      while ((boundary = pending.indexOf('\n\n')) >= 0) {
        const block = pending.slice(0, boundary); pending = pending.slice(boundary + 2);
        let event = 'message'; const data = [];
        for (const line of block.split('\n')) { if (line.startsWith('event:')) event = line.slice(6).trim(); if (line.startsWith('data:')) data.push(line.slice(5).trimStart()); }
        if (data.length) { const text = data.join('\n'); onEvent(event, JSON.parse(text)); }
      }
      if (done) break;
    }
  } finally { reader.releaseLock(); }
}
export function providers(topology) {
  const nodes = [topology?.manifest, ...(topology?.peers || []).map(peer => peer.manifest)].filter(Boolean);
  const seen = new Set();
  return nodes.flatMap(node => (node.provides || []).map(capability => ({ node, capability }))).filter(item => { const key = nodeID(item.node) + ':' + item.capability.id; if (seen.has(key)) return false; seen.add(key); return true; });
}
export function template(path) {
  const templates = {
    '/session/list': { limit: 50, offset: 0 }, '/session/context': { session_id: '', limit: 100 }, '/session/get': { session_id: '' }, '/session/delete': { session_id: '' }, '/session/expire': { session_id: '' },
    '/skills/list': { limit: 50 }, '/skills/search': { query: '', limit: 20 }, '/skills/get': { id: '', version: '' }, '/skills/delete': { id: '' }, '/skills/versions': { id: '' },
    '/skills/put': { id: '', version: '1.0.0', title: '', description: '', tags: [], files: [{ path: 'SKILL.md', content: '' }] },
    '/skills/import': { id: '', version: '1.0.0', archive: '' },
    '/skills/file': { id: '', version: '', new_version: '', path: '', content: '' }, '/skills/file/delete': { id: '', version: '', new_version: '', path: '' },
    '/secrets/list': { limit: 50 }, '/secrets/put': { key: '', value: '', note: '' }, '/secrets/delete': { key: '' }, '/workspace/create': { workspace: '' },
    '/workspace/list': { workspace: 'default', path: '' }, '/workspace/get': { workspace: 'default', path: '' }, '/workspace/put': { workspace: 'default', path: '', content: '' }, '/workspace/delete': { workspace: 'default', path: '' },
    '/events/query': { after_id: 0, limit: 100 }, '/stats/query': {}, '/benchmark/run': { model: '', include_e2e: false, session_id: '', container_id: '' },
    '/llm/config': { active_model_id: '', models: [] },
    '/telegram/config': { allowed_user_ids: [], idle_seconds: 86400 },
    '/executor/containers/action': { id: '', action: 'restart' }, '/executor/exec': { id: '', command: ['pwd'], timeout_seconds: 60, async: false },
    '/executor/files': { id: '', operation: 'list', path: '/' }, '/executor/artifacts/export': { id: '', path: '/' }, '/executor/images/pull': { image: '', approval_ids: [] },
    '/executor/deployments': { schema: 'whalemesh.deployment/v1', id: '', namespace: '', node: { name: '', labels: {}, annotations: {}, requires: [] }, requires: [], image: '', command: [], env: {}, secrets: [], volumes: [], resources: { memory_bytes: 268435456, nano_cpus: 500000000, pids_limit: 128 }, network: { name: '', egress: false, ports: [] }, replicas: 1, scope: 'global', session_id: '', task_id: '', ttl_seconds: 0, restart_policy: 'unless-stopped', desired_state: 'running', labels: {}, approval_ids: [] }, '/agent/approvals': { session_id: '', tool: '', arguments: {} }
  };
  return templates[path] || {};
}
