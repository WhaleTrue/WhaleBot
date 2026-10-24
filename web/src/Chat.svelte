<script>
  import DOMPurify from 'dompurify';
  import { marked } from 'marked';
  import { onDestroy } from 'svelte';
  import { call, consumeSSE, readResult, download, nodeID } from './api.js';
  export let providers = [];
  let sessionID = '';
  let sessions = [];
  let messages = [];
  let text = '';
  let model = '';
  let models = [];
  let traces = [];
  let usage = null;
  let attachments = [];
  let error = '';
  let busy = false;
  let controller;
  onDestroy(() => controller?.abort());
  let approval = null;
  let loadedStorage = '';
  let loadedModels = '';
  $: agent = providers.find(item => item.capability.id === 'agent.run');
  $: storage = providers.find(item => item.capability.id === 'session.store');
  $: modelProviders = providers.filter(item => item.capability.id === 'llm.models.list');
  $: modelSignature = modelProviders.map(item => nodeID(item.node) + ':' + item.capability.attributes?.model_revision).sort().join(',');
  $: if (storage && loadedStorage !== nodeID(storage.node)) { loadedStorage = nodeID(storage.node); listSessions(); }
  $: if (loadedModels !== modelSignature) { loadedModels = modelSignature; loadModels(); }
  const operation = (provider, path, method = 'POST') => provider?.capability.operations?.find(op => op.path === path && op.method === method);
  const markdown = value => DOMPurify.sanitize(marked.parse(value || ''), { FORBID_TAGS: ['style', 'iframe', 'form'], FORBID_ATTR: ['style'] });
  async function listSessions() { try { if (storage) { const op = operation(storage, '/session/list'); if (op) sessions = (await (await call(storage, op, { limit: 100 })).json()).items || []; } } catch (e) { error = e.message; } }
  async function loadModels() {
    const results = await Promise.allSettled(modelProviders.map(async provider => {
      const op = operation(provider, '/llm/models', 'GET'); if (!op) return [];
      const data = await (await call(provider, op)).json();
      return (data.models || []).map(item => ({ ...item, revision: data.revision, provider: nodeID(provider.node), label: provider.node.identity.name, key: JSON.stringify([nodeID(provider.node), item.id, data.revision]) }));
    }));
    models = results.flatMap(result => result.status === 'fulfilled' ? result.value : []);
  }
  async function openSession(id) {
    if (busy) return;
    busy = true;
    try { const data = await (await call(storage, operation(storage, '/session/get'), { session_id: id })).json(); sessionID = id; messages = data.messages || []; traces = []; attachments = []; usage = data.usage; }
    catch (e) { error = e.message; } finally { busy = false; }
  }
  async function removeSession(id) {
    if (busy || !confirm('Delete this session?')) return;
    busy = true; let deleted = false;
    try { await call(storage, operation(storage, '/session/delete'), { session_id: id }); deleted = true; await listSessions(); }
    catch (e) { error = e.message; } finally { busy = false; if (deleted && sessionID === id) newSession(); }
  }
  function newSession() { if (busy) return; sessionID = ''; messages = []; traces = []; usage = null; attachments = []; approval = null; }
  async function send() {
    if (!text.trim() || busy || !agent) return;
    const selected = models.find(item => item.key === model);
    if (model && !selected) { error = 'Selected model is unavailable. Choose a provider and model again.'; return; }
    const content = text; text = ''; error = ''; busy = true; traces = []; attachments = []; approval = null;
    messages = [...messages, { role: 'user', content }, { role: 'assistant', content: '' }]; controller = new AbortController(); let completed = false;
    try {
      const response = await call(agent, operation(agent, '/agent/run'), { session_id: sessionID, message: content, model: selected?.id || '', provider: selected?.provider || '', model_revision: selected?.revision || '', stream: true }, { stream: true, signal: controller.signal });
      await consumeSSE(response, (event, data) => {
        if (event === 'trace') { traces = [...traces, data]; if (data.session_id) sessionID = data.session_id; if (data.event === 'approval_required') approval = data; }
        if (event === 'delta') { messages[messages.length - 1].content += data.content || ''; messages = [...messages]; }
        if (event === 'done') { completed = true; sessionID = data.session_id; messages[messages.length - 1].content = data.reply; messages = [...messages]; usage = data.usage; attachments = data.attachments || []; }
        if (event === 'error') { completed = true; error = `${data.code || 'Agent error'}: ${data.message}`; }
      });
      if (!completed) error = 'Agent stream ended without a result.';
    } catch (e) { error = e.name === 'AbortError' ? 'Run cancelled.' : e.message; } finally { busy = false; controller = null; await listSessions(); }
  }
  async function approve() { try { const provider = providers.find(item => item.capability.id === 'agent.approve'); const op = operation(provider, '/agent/approvals'); if (!op) throw new Error('Approval capability unavailable'); await call(provider, op, { session_id: sessionID, tool: approval.tool, arguments: approval.arguments }); approval = null; error = ''; } catch (e) { error = e.message; } }
  async function artifact(attachment) {
    try {
      const path = attachment.url; if (!path?.startsWith('/') || path.startsWith('//')) throw new Error('Artifact has no authenticated Mesh download operation');
      const origin = new URL(path, window.location.origin).searchParams.get('provider');
      const provider = providers.find(item => item.capability.id === 'artifact.export' && (!origin || item.node.identity.principal === origin) && item.capability.operations?.some(op => op.method === 'GET' && op.path === path.split('?')[0]));
      if (!provider) throw new Error('Artifact provider unavailable');
      const result = await readResult(await call(provider, provider.capability.operations.find(op => op.method === 'GET' && op.path === path.split('?')[0]), {}, { path }));
      if (result.download) download(result.download, attachment.name || result.filename); else throw new Error('Artifact response is not a file');
    } catch (e) { error = e.message; }
  }
</script>
<div class="chat-layout"><aside class="panel sessions"><div class="row spread"><h3>Sessions</h3><button disabled={busy} on:click={newSession}>New</button></div><button on:click={listSessions}>Refresh sessions</button>{#each sessions as session}<div class="session"><button disabled={busy} class:active={session.session_id === sessionID} on:click={() => openSession(session.session_id)} title={session.session_id}>{session.metadata?.title || session.messages?.[0]?.content?.slice(0, 45) || session.session_id}</button><button disabled={busy} class="danger" aria-label="Delete session" on:click={() => removeSession(session.session_id)}>×</button></div>{/each}</aside>
<section class="panel chat"><div class="row spread"><div><h2>Chat</h2><small>{sessionID || 'New private session'}</small></div><label>Model<select bind:value={model}><option value="">Automatic free model only</option>{#each models as item}<option value={item.key}>{item.label} / {item.name || item.id} ({item.cost_class || 'unknown cost'})</option>{/each}</select></label><button on:click={loadModels}>Refresh models</button></div>
{#if !agent}<div class="notice">No ready agent.run capability. Chat becomes available when a trusted provider joins.</div>{/if}
<div class="messages" aria-live="polite">{#if !messages.length}<p class="hint">Start a conversation. Sessions belong to your browser user, not this node's Mesh principal.</p>{/if}{#each messages as message}<article class:assistant={message.role === 'assistant'}><strong>{message.role}</strong><div class="markdown">{@html markdown(message.content)}</div></article>{/each}</div>
{#if attachments.length}<div class="row">{#each attachments as attachment}<button on:click={() => artifact(attachment)}>Download {attachment.name}</button>{/each}</div>{/if}
{#if error}<p class="error" role="alert">{error}</p>{/if}
<form on:submit|preventDefault={send}><label class="sr-only" for="message">Message</label><textarea id="message" bind:value={text} rows="3" disabled={busy} on:keydown={event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); send(); } }}></textarea><div class="row spread"><span class="hint">Enter sends. Shift+Enter adds a line.</span>{#if busy}<button type="button" disabled={!controller} class="danger" on:click={() => controller?.abort()}>{controller ? 'Cancel' : 'Loading'}</button>{:else}<button disabled={!agent || !text.trim()}>Send</button>{/if}</div></form>
{#if usage}<p class="hint">Tokens: {usage.total_tokens || 0} · input {usage.prompt_tokens || 0} · output {usage.completion_tokens || 0}</p>{/if}
<details open={busy}><summary>Progress and tool trace ({traces.length})</summary>{#each traces as trace}<div class="trace"><strong>{trace.event}</strong> {trace.tool || ''} <small>{trace.duration_ms ? `${trace.duration_ms} ms` : ''}</small><pre>{JSON.stringify(trace, null, 2)}</pre></div>{/each}</details>
{#if approval}<section class="notice"><h3>Tool approval required</h3><p>Review exact arguments. Approval is scoped to this user, session and tool. Only users with agent.approve scope can grant it.</p><pre>{JSON.stringify(approval, null, 2)}</pre><button on:click={approve}>Approve exact call once</button></section>{/if}
</section></div>
