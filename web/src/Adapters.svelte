<script>
  import { call, nodeID } from './api.js';
  export let providers = [];
  let selected = '';
  let loaded = '';
  let token = '';
  let allowed = '';
  let idle = 86400;
  let configuration = null;
  let status = null;
  let error = '';
  let saved = '';
  let clearToken = false;
  $: candidates = providers.filter(item => item.capability.id === 'adapter.telegram');
  $: if (candidates.length && !candidates.some(item => nodeID(item.node) === selected)) selected = nodeID(candidates[0].node);
  $: provider = candidates.find(item => nodeID(item.node) === selected) || candidates[0];
  $: if (provider && nodeID(provider.node) !== loaded) { loaded = nodeID(provider.node); load(); }
  const operation = name => provider.capability.operations.find(op => op.name === name);
  async function load() {
    const current = provider;
    try {
      error = '';
      const [settings, health] = await Promise.all(['config.get', 'status'].map(async name => (await call(current, current.capability.operations.find(op => op.name === name))).json()));
      if (!provider || nodeID(current.node) !== nodeID(provider.node)) return;
      configuration = settings; status = health; allowed = (settings.allowed_user_ids || []).join(', '); idle = settings.idle_seconds;
    } catch (e) { if (provider && nodeID(current.node) === nodeID(provider.node)) error = e.message; }
  }
  async function save() {
    try {
      error = ''; saved = '';
      const users = allowed.split(',').map(value => value.trim()).filter(Boolean).map(value => { const number = Number(value); if (!Number.isSafeInteger(number) || number <= 0) throw new Error('Whitelist requires positive integer Telegram user IDs'); return number; });
      const input = { allowed_user_ids: users, idle_seconds: idle }; if (token || clearToken) input.token = clearToken ? '' : token;
      await call(provider, operation('config.put'), input); token = ''; clearToken = false; saved = 'Adapter configuration saved. Poller restarts with the new settings.'; await load();
    } catch (e) { error = e.message; }
  }
</script>
<section class="panel"><h2>Telegram</h2><p>Outbound long polling. No inbound webhook or public port. Token and whitelist persist privately.</p>{#if !provider}<div class="notice">No Telegram configuration capability is available.</div>{:else}
<div class="row"><label>Adapter node<select bind:value={selected}>{#each candidates as item}<option value={nodeID(item.node)}>{item.node.identity.name}</option>{/each}</select></label><button on:click={load}>Refresh status</button><span class="badge">{status?.state || 'Loading status'}</span></div>
{#if status?.last_error}<p class="error">{status.last_error}</p>{/if}
{#if configuration}<form on:submit|preventDefault={save}><label>Bot token {configuration.has_token ? '(stored)' : '(not configured)'}<input type="password" bind:value={token} autocomplete="new-password" /></label><label><input type="checkbox" bind:checked={clearToken} style="display:inline;width:auto" /> Remove stored token</label><label>Allowed user IDs (comma separated)<input bind:value={allowed} /></label><p class="hint">Empty whitelist disables polling. It never permits anonymous or unrestricted access.</p><label>Idle session timeout (seconds)<input type="number" min="60" bind:value={idle} required /></label><button>Save adapter</button></form><p>{status?.conversations || 0} persisted conversations. Commands: /new, /end, /status, /help.</p>{/if}{/if}
{#if error}<p class="error" role="alert">{error}</p>{/if}{#if saved}<p role="status">{saved}</p>{/if}</section>
