<script>
  import { call, readResult, download, template, nodeID } from './api.js';
  export let title;
  export let description = '';
  export let providers = [];
  let selectedKey = '';
  let operationName = '';
  let input = '{}';
  let path = '';
  let result = null;
  let error = '';
  let busy = false;
  let filePath = '';
  let content = '';
  let loadedKey = '';
  let loadedPath = '';
  const key = item => `${nodeID(item.node)}:${item.capability.id}`;
  $: if (providers.length && !providers.some(item => key(item) === selectedKey)) selectedKey = key(providers[0]);
  $: selected = providers.find(item => key(item) === selectedKey) || providers[0];
  $: operations = (selected?.capability.operations || []).filter(op => op.path !== '/secrets/resolve');
  $: if (operations.length && !operations.some(item => item.name === operationName)) operationName = operations[0].name;
  $: operation = operations.find(op => op.name === operationName) || operations[0];
  $: currentKey = selected && operation ? key(selected) + ':' + operation.name : '';
  $: if (currentKey && currentKey !== loadedKey) { loadedKey = currentKey; if (operation.path !== loadedPath) input = JSON.stringify(template(operation.path), null, 2); loadedPath = operation.path; path = operation.path; result = null; }
  $: items = Array.isArray(result) ? result : result?.items || result?.containers || result?.models || result?.runs || result?.events || [];
  async function execute() {
    error = ''; busy = true;
    try {
      const body = JSON.parse(input);
      if (!['GET', 'HEAD'].includes(operation.method) && (operation.semantics?.side_effecting || operation.semantics?.mutating) && !confirm(`Execute ${operation.name} on ${selected.node.identity.name}?`)) return;
      result = await readResult(await call(selected, operation, body, { path }));
      if (result?.download) download(result.download, result.filename);
    } catch (e) { error = e.message; } finally { busy = false; }
  }
  function loadResult() { input = JSON.stringify(result, null, 2); }
  function chooseItem(item) {
    const body = JSON.parse(input);
    for (const field of ['id', 'session_id', 'version', 'workspace', 'path']) if (item[field] !== undefined) body[field] = item[field];
    input = JSON.stringify(body, null, 2);
  }
  async function importFile(event) {
    const file = event.target.files?.[0]; if (!file) return;
    try { const bytes = new Uint8Array(await file.arrayBuffer()); let text = ''; for (let i = 0; i < bytes.length; i += 8192) text += String.fromCharCode(...bytes.subarray(i, i + 8192)); content = btoa(text); filePath = file.name; } catch (e) { error = e.message; }
  }
  function insertContent() {
    try { const body = JSON.parse(input); if (operation.path === '/skills/put') body.files = [...(body.files || []).filter(f => f.path !== filePath), { path: filePath, content }]; else if (operation.path === '/skills/import') body.archive = content; else if (operation.path === '/executor/files') { body.content_base64 = content; body.path = filePath; body.operation = 'write'; } else { body.content = content; body.path = filePath; } input = JSON.stringify(body, null, 2); } catch (e) { error = e.message; }
  }
  function downloadContent(value, name) { try { const raw = atob(value); download(new Blob([Uint8Array.from(raw, char => char.charCodeAt(0))]), name || 'file'); } catch (e) { error = e.message; } }
</script>
<section class="panel">
  <h2>{title}</h2><p>{description}</p>
  {#if !providers.length}<div class="notice">No ready provider advertises this capability. Add a trusted node with this capability. Discovery updates automatically.</div>
  {:else}
    <div class="row"><label>Provider<select bind:value={selectedKey}>{#each providers as item}<option value={key(item)}>{item.node.identity.name} · {item.capability.id}</option>{/each}</select></label><label>Operation<select bind:value={operationName}>{#each operations as op}<option value={op.name}>{op.method} {op.name} · {op.path}</option>{/each}</select></label><span class="badge">{selected?.capability.version}</span></div>
    {#if operation}
    <form on:submit|preventDefault={execute}><label>Operation path and query<input bind:value={path} required spellcheck="false" /></label><div class="columns"><label>Request JSON<textarea bind:value={input} rows="16" spellcheck="false" aria-label="Capability request JSON"></textarea></label><div><h4>Published contract</h4><p>Scopes: {(operation.scopes || []).join(', ') || 'No operation authorization published; browser calls denied'}</p><pre>{JSON.stringify({ operation, input_schema: selected.capability.input_schema }, null, 2)}</pre><p class="hint">User identity is injected by the backend. Browser input cannot change owner. Only advertised operations are callable.</p></div></div><button disabled={busy || !operation}>{busy ? 'Running…' : `Execute ${operation.name}`}</button></form>
    {#if /skills|workspace|files/.test(operation.path)}<details><summary>File content editor / upload</summary><div class="row"><label>File path<input bind:value={filePath} /></label><label>Upload file<input type="file" on:change={importFile} /></label></div><label>Base64 content<textarea bind:value={content} rows="4"></textarea></label><div class="row"><button on:click={insertContent}>Insert in request</button><button on:click={() => { content = btoa(unescape(encodeURIComponent(content))); }}>Encode text to base64</button></div></details>{/if}
    {/if}
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>
{#if result !== null}<section class="panel"><div class="row spread"><h3>Response</h3><div class="row"><button on:click={loadResult}>Use response as request</button><button on:click={() => download(new Blob([JSON.stringify(result, null, 2)], { type: 'application/json' }), `${title.toLowerCase().replaceAll(' ', '-')}.json`)}>Download JSON</button></div></div>
  {#if result?.download}<p>Downloaded {result.filename} ({result.download.size} bytes).</p>{:else}
  {#if items.length}<div class="items">{#each items as item}<button class="item" on:click={() => chooseItem(item)}><strong>{item.title || item.name || item.session_id || item.id || item.kind || 'Record'}</strong><small>{item.description || item.image || item.updated_at || item.version || ''}</small></button>{/each}</div>{/if}
  {#if result.content}<button on:click={() => downloadContent(result.content, result.path)}>Download file content</button>{/if}
  {#if result.content_base64}<button on:click={() => downloadContent(result.content_base64, result.path)}>Download file content</button>{/if}
  <pre>{JSON.stringify(result, null, 2)}</pre>{/if}</section>{/if}
