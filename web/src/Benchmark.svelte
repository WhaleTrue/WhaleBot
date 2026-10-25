<script>
  import { onMount, onDestroy } from 'svelte';
  import { call, download, nodeID } from './api.js';
  import { benchmarkHistory, benchmarkScore, benchmarkCSV } from './benchmark.js';
  import Resource from './Resource.svelte';
  export let providers = [];
  let selected = '';
  let loaded = '';
  let generation = 0;
  let runs = [];
  let busy = false;
  let refreshing = false;
  let stopped = false;
  let error = '';
  let message = '';
  let model = '';
  let models = [];
  let modelSignature = '';
  let modelGeneration = 0;
  let modelError = '';
  let includeE2E = false;
  let sessionID = '';
  let containerID = '';
  let prepared = null;
  let approved = false;
  let preparedKey = '';
  let expanded = '';
  const operation = (item, path, method) => item?.capability.operations?.find(op => op.path === path && op.method === method);
  $: benchmarkProviders = providers.filter(item => item.capability.id.startsWith('benchmark.'));
  $: candidates = benchmarkProviders.filter(item => operation(item, '/benchmark/runs', 'GET'));
  $: if (!selected && candidates.length) selected = nodeID(candidates[0].node);
  $: provider = candidates.find(item => nodeID(item.node) === selected);
  $: runner = benchmarkProviders.find(item => nodeID(item.node) === selected && operation(item, '/benchmark/run', 'POST'));
  $: preparer = benchmarkProviders.find(item => nodeID(item.node) === selected && operation(item, '/benchmark/e2e/prepare', 'POST'));
  $: approver = providers.find(item => nodeID(item.node) === selected && operation(item, '/agent/approvals', 'POST'));
  $: if ((provider ? nodeID(provider.node) : '') !== loaded) { loaded = provider ? nodeID(provider.node) : ''; generation++; runs = []; error = ''; expanded = ''; if (provider) refresh(); }
  $: preparationKey = JSON.stringify([selected, sessionID, containerID, includeE2E]);
  $: if (preparationKey !== preparedKey) { preparedKey = preparationKey; prepared = null; approved = false; }
  $: history = benchmarkHistory(runs);
  $: running = runs.some(run => run.status === 'running');
  $: modelProviders = providers.filter(item => item.capability.id === 'llm.models.list');
  $: signature = modelProviders.map(item => nodeID(item.node) + ':' + item.capability.attributes?.model_revision).sort().join(',');
  $: if (signature !== modelSignature) { modelSignature = signature; loadModels(); }
  async function loadModels() {
    const current = ++modelGeneration;
    const results = await Promise.allSettled(modelProviders.map(async item => {
      const op = operation(item, '/llm/models', 'GET');
      if (!op) return [];
      const data = await (await call(item, op)).json();
      return (data.models || []).map(profile => ({ ...profile, provider: nodeID(item.node), label: item.node.identity.name, key: JSON.stringify([nodeID(item.node), profile.id, data.revision]) }));
    }));
    if (current !== modelGeneration) return;
    models = results.flatMap(result => result.status === 'fulfilled' ? result.value : []);
    modelError = results.filter(result => result.status === 'rejected').map(result => result.reason.message).join('; ');
  }
  async function refresh() {
    if (!provider || refreshing || stopped) return;
    const current = provider; const version = generation;
    refreshing = true;
    try {
      const data = await (await call(current, operation(current, '/benchmark/runs', 'GET'))).json();
      if (version === generation) { runs = data.runs || []; error = ''; }
    } catch (e) { if (version === generation) error = e.message; }
    finally { refreshing = false; if (version !== generation && provider && !stopped) refresh(); }
  }
  async function prepare() {
    busy = true; error = ''; prepared = null; approved = false;
    const key = preparationKey;
    try {
      const result = await (await call(preparer, operation(preparer, '/benchmark/e2e/prepare', 'POST'), { session_id: sessionID, container_id: containerID })).json();
      if (key === preparationKey) prepared = result;
    } catch (e) { error = e.message; } finally { busy = false; }
  }
  async function approve() {
    if (!confirm('Approve both exact E2E tool calls once each? Review the build and verification commands and target first.')) return;
    busy = true; error = '';
    try {
      await call(approver, operation(approver, '/agent/approvals', 'POST'), { session_id: prepared.session_id, tool: prepared.tool, arguments: prepared.arguments });
      await call(approver, operation(approver, '/agent/approvals', 'POST'), { session_id: prepared.session_id, tool: prepared.verification_tool, arguments: prepared.verification_arguments });
      approved = true;
    } catch (e) { error = e.message; } finally { busy = false; }
  }
  async function start() {
    const chosen = models.find(item => item.key === model);
    if (model && !chosen) { error = 'Selected model is unavailable. Refresh models and select it again.'; return; }
    if (!confirm(`Run benchmark${includeE2E ? ' with real E2E execution' : ''}? Explicit model choices may incur inference costs.`)) return;
    busy = true; error = ''; message = '';
    try {
      const input = { model: chosen?.id || '', provider: chosen?.provider || '', include_e2e: includeE2E };
      if (includeE2E) Object.assign(input, { session_id: prepared.session_id, container_id: prepared.container_id, e2e_nonce: prepared.e2e_nonce });
      const run = await (await call(runner, operation(runner, '/benchmark/run', 'POST'), input)).json();
      message = `Run ${run.id} started. Results refresh while running.`;
      prepared = null; approved = false;
      await refresh();
    } catch (e) { error = e.message; } finally { busy = false; }
  }
  async function remove(run) {
    if (!confirm(run ? `Delete benchmark ${run.id}?` : 'Delete all benchmark history for this user on this provider?')) return;
    busy = true; error = '';
    try {
      const op = operation(provider, run ? '/benchmark/runs/{id}' : '/benchmark/runs', 'DELETE');
      await call(provider, op, {}, run ? { path: '/benchmark/runs/' + encodeURIComponent(run.id) } : {});
      expanded = ''; await refresh();
    } catch (e) { error = e.message; } finally { busy = false; }
  }
  const score = (run, key) => benchmarkScore(run, key) === null ? '—' : Math.round(benchmarkScore(run, key));
  const date = value => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString(undefined, { hour12: false }) : '—';
  const exportJSON = run => download(new Blob([JSON.stringify(run, null, 2)], { type: 'application/json' }), `benchmark-${run?.id || 'runs'}.json`);
  onMount(() => { const timer = setInterval(() => { if (running && !busy) refresh(); }, 3000); return () => clearInterval(timer); });
  onDestroy(() => { stopped = true; generation++; modelGeneration++; });
</script>

<section class="panel">
  <h2>Model benchmark</h2>
  <p>Daily score keeps Plan gate, Tool call and ReAct separate from Bonus and real E2E. Compare scores only within the same case set.</p>
  <div class="row">
    <label>Benchmark provider<select bind:value={selected} disabled={busy || refreshing}><option value="" disabled>Select a provider</option>{#each candidates as item}<option value={nodeID(item.node)}>{item.node.identity.name} · {item.node.identity.principal}</option>{/each}</select></label>
    <button on:click={refresh} disabled={!provider || busy || refreshing}>{refreshing ? 'Refreshing…' : 'Refresh history'}</button>
  </div>
  {#if !provider}<div class="notice">No selected benchmark results provider is available. Choose a trusted provider. History is never silently moved to another provider.</div>{:else}
    <form on:submit|preventDefault={start}>
      <label>Model<select bind:value={model} disabled={busy}><option value="">Automatic active free model only</option>{#each models as item}<option value={item.key}>{item.label} / {item.name || item.id} ({item.cost_class || 'unknown cost'})</option>{/each}</select></label>
      <p class="hint">Automatic selection uses only an explicitly free active profile. Select a provider/model to authorize paid or unknown-cost inference. Each run freezes its actual model revision.</p>
      <button type="button" on:click={loadModels} disabled={busy}>Refresh models</button>
      <label class="check"><input type="checkbox" bind:checked={includeE2E} disabled={busy} />Include real E2E</label>
      {#if includeE2E}
        <p class="hint">Use an existing user-owned Go build container and a dedicated session. Prepare fresh nonce-bound build and verification commands, review both, then grant one scoped approval for each. E2E builds and runs in separate directories within this one container, not a two-container deployment. No browser boolean grants Docker permission.</p>
        <div class="row"><label>Session ID<input bind:value={sessionID} disabled={busy} required /></label><label>Approved container ID<input bind:value={containerID} disabled={busy} required /></label></div>
        <button type="button" on:click={prepare} disabled={busy || running || !preparer || !sessionID || !containerID}>Prepare E2E command</button>
        {#if !preparer}<p class="notice">Selected provider does not advertise E2E preparation.</p>{/if}
        {#if prepared}
          <h3>Review exact E2E calls</h3><pre>{JSON.stringify(prepared, null, 2)}</pre>
          <button type="button" on:click={approve} disabled={busy || approved || !approver}>{approved ? 'Both exact calls approved once each' : 'Approve both exact calls once each'}</button>
          {#if !approver}<p class="notice">This provider does not advertise the required approval operation.</p>{/if}
        {/if}
      {/if}
      <div class="row"><button disabled={busy || running || !runner || (includeE2E && (!prepared || !approved))}>{busy ? 'Working…' : 'Run benchmark'}</button>{#if running}<span role="status">A run is in progress. History refreshes every 3 seconds.</span>{/if}</div>
    </form>
  {/if}
  {#if modelError}<p class="error" role="alert">Model catalog: {modelError}</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if message}<p role="status">{message}</p>{/if}
</section>

<section class="panel">
  <div class="row spread"><h2>History</h2><div class="row">
    <button disabled={!runs.length} on:click={() => download(new Blob([benchmarkCSV(history.runs)], { type: 'text/csv;charset=utf-8' }), 'benchmark-runs.csv')}>Download CSV</button>
    <button disabled={!runs.length} on:click={() => exportJSON(history.runs)}>Download all JSON</button>
    <button class="danger" disabled={busy || refreshing || running || !runs.length || !operation(provider, '/benchmark/runs', 'DELETE')} on:click={() => remove(null)}>Clear history</button>
  </div></div>
  {#if history.latestCaseSet}<p>Latest case set: <strong class="badge latest">{history.latestCaseSet}</strong>. Best marks completed runs in this case set only. Older versions remain available below.</p>{/if}
  <div class="table-scroll" role="region" aria-label="Benchmark history, scroll horizontally for all columns" aria-busy={refreshing}>
    <table>
      <caption class="sr-only">Persisted benchmark results for the selected provider. Scores range from 0 to 100. A dash means unavailable or incomplete.</caption>
      <thead><tr><th scope="col">Model / provider</th><th scope="col">Case set</th><th scope="col">Status / started</th><th scope="col">Daily score</th><th scope="col">Bonus</th><th scope="col">E2E</th><th scope="col">Tokens</th><th scope="col">Actions</th></tr></thead>
      <tbody>{#each history.runs as run (run.id)}
        <tr>
          <th scope="row"><span class="model-name">{run.model || 'Not selected yet'}</span>{#if run.id === history.bestId}<span class="badge">Best · {history.latestCaseSet}</span>{/if}<small class="model-name">{run.upstream_model || ''}</small><small class="model-name">{run.provider || ''}</small></th>
          <td>{#if run.case_set && run.case_set === history.latestCaseSet}<strong class="badge latest">{run.case_set}</strong>{:else}<span class="historical">{run.case_set || 'Unversioned'}</span><small>Historical</small>{/if}</td>
          <td><strong>{run.status}</strong><small>{date(run.started_at)}</small></td>
          <td class="daily"><strong>{score(run, 'total')}</strong><small>Plan gate {score(run, 'plan_gate')} · Tool call {score(run, 'tool_call')} · ReAct {score(run, 'react')}</small></td>
          <td class="number">{score(run, 'bonus')}</td><td class="number">{score(run, 'e2e')}<small>{(run.cases || []).some(item => item.category === 'e2e') ? 'See checkpoints' : 'Not run'}</small></td>
          <td class="number">{run.usage?.total_tokens ?? '—'}</td>
          <td><div class="row actions"><button aria-expanded={expanded === run.id} aria-controls={'details-' + run.id} on:click={() => expanded = expanded === run.id ? '' : run.id}>Details</button><button on:click={() => exportJSON(run)}>JSON</button><button class="danger" disabled={busy || refreshing || run.status === 'running' || !operation(provider, '/benchmark/runs/{id}', 'DELETE')} on:click={() => remove(run)}>Delete</button></div></td>
        </tr>
        {#if expanded === run.id}<tr><td colspan="8" id={'details-' + run.id}>
          <h3>Run {run.id}</h3><p>Model revision: <code>{run.model_revision || 'Unavailable'}</code> · Finished: {date(run.finished_at)}</p>
          {#if run.error}<p class="error" role="alert">{run.error}</p>{/if}
          <table class="cases"><thead><tr><th scope="col">Category / case</th><th scope="col">Result</th><th scope="col">Score</th><th scope="col">Latency</th><th scope="col">Evidence / error</th></tr></thead><tbody>{#each run.cases || [] as item}<tr><th scope="row">{item.category}<small>{item.name}</small></th><td>{item.pass ? 'Passed' : 'Failed'}</td><td>{Number.isFinite(item.score) ? Math.round(item.score * 100) + '%' : '—'}</td><td>{item.latency_ms ?? '—'} ms</td><td class="evidence">{item.detail || '—'}</td></tr>{/each}</tbody></table>
          <details><summary>Full run JSON, usage and evidence</summary><pre>{JSON.stringify(run, null, 2)}</pre></details>
        </td></tr>{/if}
      {:else}<tr><td colspan="8">{refreshing ? 'Loading persisted history…' : 'No persisted results. Choose a model and run a benchmark.'}</td></tr>{/each}</tbody>
    </table>
  </div>
</section>
<details><summary>Advanced capability JSON console</summary><Resource title="Benchmark" description="Inspect advertised contracts and scopes, send structured JSON and download exact responses. The backend enforces user ownership and operation authorization." providers={benchmarkProviders} /></details>

<style>
  .check{display:flex;align-items:center;gap:.6rem;margin:1rem 0}.check input{width:auto;margin:0}.table-scroll{overflow-x:auto;max-width:100%}table{width:100%;border-collapse:collapse;text-align:left;font-size:.85rem}th,td{padding:.9rem .7rem;border-bottom:1px solid #28374f;vertical-align:top}thead th{color:#aebed7;font-weight:600;white-space:nowrap}tbody th{font-weight:500}small{display:block;margin-top:.3rem}.number,.daily{font-variant-numeric:tabular-nums;white-space:nowrap}.daily>strong{font-size:1.25rem}.latest{font-size:.9rem;font-weight:750;border-color:#5ddbb5}.historical{color:#a9bad4;font-size:.8rem}.model-name{display:block;max-width:16rem;overflow-wrap:anywhere}.actions{min-width:16rem;gap:.4rem}.actions button{min-height:44px}.cases{font-size:.8rem}.evidence{max-width:40rem;white-space:pre-wrap;overflow-wrap:anywhere}h3{margin-top:1rem}form>.row{margin-top:1rem}
</style>
