<script>
  import { call, nodeID } from './api.js';
  export let providers = [];
  let selected = '';
  let loaded = '';
  let loadGeneration = 0;
  let configuration = { active_model_id: '', models: [] };
  let error = '';
  let message = '';
  let busy = false;
  $: candidates = providers.filter(item => item.capability.operations?.some(op => op.path === '/llm/config' && op.method === 'GET'));
  $: if (candidates.length && !candidates.some(item => nodeID(item.node) === selected)) selected = nodeID(candidates[0].node);
  $: provider = candidates.find(item => nodeID(item.node) === selected) || candidates[0];
  $: if (provider && nodeID(provider.node) !== loaded) { loaded = nodeID(provider.node); load(); }
  async function load() {
    const generation = ++loadGeneration;
    const current = provider;
    busy = true; error = '';
    try {
      const result = await (await call(current, current.capability.operations.find(op => op.path === '/llm/config' && op.method === 'GET'))).json();
      if (generation === loadGeneration && provider && nodeID(current.node) === nodeID(provider.node)) configuration = result;
    } catch (e) { if (generation === loadGeneration) error = e.message; }
    finally { if (generation === loadGeneration) busy = false; }
  }
  function add() { configuration = { ...configuration, models: [...configuration.models, { id: crypto.randomUUID(), name: '', base_url: 'https://api.openai.com/v1', model: '', timeout_seconds: 120, cost_class: 'unknown', capabilities: [], api_key: '' }] }; }
  function remove(index) { const removed = configuration.models[index]; configuration = { ...configuration, active_model_id: configuration.active_model_id === removed.id ? '' : configuration.active_model_id, models: configuration.models.filter((_, i) => i !== index) }; }
  async function save() {
    busy = true; error = ''; message = '';
    try {
      const models = configuration.models.map(item => { const result = { id: item.id, name: item.name, base_url: item.base_url, model: item.model, timeout_seconds: item.timeout_seconds, cost_class: item.cost_class || 'unknown', capabilities: item.capabilities || [] }; if (item.api_key !== undefined && item.api_key !== '') result.api_key = item.api_key; return result; });
      await call(provider, provider.capability.operations.find(op => op.path === '/llm/config' && op.method === 'PUT'), { active_model_id: configuration.active_model_id, models });
      message = 'Model configuration saved.'; await load();
    } catch (e) { error = e.message; } finally { busy = false; }
  }
</script>
<section class="panel"><h2>Model configuration</h2><p>Explicit active model. No silent provider switching. Keys are write-only; blank fields preserve existing keys.</p>
{#if !provider}<div class="notice">No model configuration capability is available.</div>{:else}
<div class="row"><label>Provider<select bind:value={selected} disabled={busy}>{#each candidates as item}<option value={nodeID(item.node)}>{item.node.identity.name}</option>{/each}</select></label><button on:click={load} disabled={busy}>Reload</button></div>
<form on:submit|preventDefault={save}><label>Active model<select bind:value={configuration.active_model_id}><option value="">No active model</option>{#each configuration.models as model}<option value={model.id}>{model.name || model.id}</option>{/each}</select></label>
{#each configuration.models as model, index}<section class="panel"><div class="row spread"><h3>{model.name || 'Model'}</h3><button type="button" class="danger" on:click={() => remove(index)}>Remove</button></div><div class="columns"><label>ID<input bind:value={model.id} required /></label><label>Display name<input bind:value={model.name} required /></label><label>Base URL<input bind:value={model.base_url} required /></label><label>Provider model<input bind:value={model.model} required /></label><label>Timeout seconds<input type="number" bind:value={model.timeout_seconds} min="1" max="1800" required /></label><label>Cost class<select bind:value={model.cost_class}><option value="unknown">Unknown: explicit selection only</option><option value="free">Free</option><option value="paid">Paid: explicit selection only</option></select></label><label>Capabilities<select multiple bind:value={model.capabilities}><option value="tools">Tool calling</option><option value="streaming">Streaming</option></select></label><label>API key {model.has_api_key ? '(stored)' : '(not stored)'}<input type="password" bind:value={model.api_key} autocomplete="new-password" /></label></div></section>{/each}
<div class="row"><button type="button" on:click={add}>Add model</button><button disabled={busy}>{busy ? 'Saving…' : 'Save models'}</button></div></form>{/if}
{#if error}<p class="error" role="alert">{error}</p>{/if}{#if message}<p role="status">{message}</p>{/if}</section>
