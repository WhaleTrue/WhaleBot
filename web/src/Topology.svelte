<script>
  import { tick } from 'svelte';
  import { json } from './api.js';
  import { buildTopology, providerReasons } from './topology.js';
  export let topology;
  export let refresh;
  let selected = null;
  let selectedTopology;
  let inspection;
  let nodeKey = '';
  let dependencyFilter = 'all';
  let error = '';
  let saving = false;
  let requirement = '';
  let editedRequirement = '';
  let policy = '';
  let pin = '';
  $: graph = buildTopology(topology);
  $: policies = topology?.policies || [];
  $: localRequirements = graph.requirements.filter(item => item.node.local);
  $: if (topology !== selectedTopology) {
    selectedTopology = topology;
    selected = null;
    editedRequirement = '';
  }
  $: if (!graph.nodes.some(node => node.key === nodeKey)) nodeKey = graph.nodes[0]?.key || '';
  $: focusedNode = graph.nodes.find(node => node.key === nodeKey);
  $: capabilities = graph.capabilities.filter(item => item.node === focusedNode);
  $: requirements = graph.requirements.filter(item => item.node === focusedNode);
  $: visibleRequirements = requirements.filter(item => dependencyFilter === 'all' || (dependencyFilter === 'optional') === item.optional);
  $: incomingDeclarations = graph.declarations.filter(edge => edge.target.node === focusedNode);
  $: recorded = graph.recorded.filter(edge => edge.source?.node === focusedNode || edge.source === focusedNode || edge.target.node === focusedNode);
  $: previews = graph.previews.filter(edge => edge.source?.node === focusedNode || edge.source === focusedNode || edge.target.node === focusedNode);
  $: routes = graph.routes.filter(edge => edge.source === focusedNode || edge.target === focusedNode);
  $: if (requirement !== editedRequirement) {
    editedRequirement = requirement;
    const explanation = topology?.resolutions?.[requirement];
    const effective = explanation?.policy || explanation?.requirement?.policy;
    policy = policies.includes(effective) ? effective : '';
    pin = explanation?.requirement?.pin || '';
  }
  async function inspect(kind, data) {
    selected = { kind, data };
    await tick();
    inspection?.focus();
  }
  function focusNode(key) {
    nodeKey = key;
    selected = null;
  }
  async function savePolicy() {
    try {
      error = ''; saving = true;
      await json('/api/policy', 'PUT', { requirement, policy, pin });
      await refresh();
    } catch (e) { error = e.message; }
    finally { saving = false; }
  }
</script>

<div class="topology-browser">
  <section class="panel">
    <div class="section-heading">
      <div><h2>Capability topology</h2><p>Explore one node at a time. Browsing, filtering and refreshing never resolve or select providers.</p></div>
      <button type="button" on:click={refresh}>Refresh snapshot</button>
    </div>
    <label class="node-picker">Focus node · {graph.nodes.length} in snapshot
      <select bind:value={nodeKey} on:change={() => selected = null} disabled={!graph.nodes.length}>
        {#each graph.nodes as node}
          <option value={node.key}>{node.manifest.identity.name} · {node.local ? 'local' : node.snapshotOnly ? 'binding snapshot only' : 'peer'} · {node.manifest.identity.instance_id}</option>
        {/each}
      </select>
    </label>
    {#if focusedNode}
      <div class="node-heading">
        <div><h3>{focusedNode.manifest.identity.name}</h3><p class="identity">{focusedNode.manifest.identity.namespace} / {focusedNode.manifest.identity.principal} / {focusedNode.manifest.identity.instance_id}</p></div>
        <button type="button" on:click={() => inspect(focusedNode.snapshotOnly ? 'Recorded/preview snapshot node — not a current peer' : 'Current node manifest', focusedNode.manifest)}>Inspect node JSON</button>
      </div>
      <p class="node-state">{focusedNode.local ? 'Local node · ' : ''}<span class:warning={focusedNode.snapshotOnly || !focusedNode.manifest.lifecycle?.ready}>{focusedNode.snapshotOnly ? 'Binding snapshot only — not a current peer' : focusedNode.manifest.lifecycle?.ready ? 'Node ready' : 'Node not ready'}</span></p>

      <section class="relation-section" aria-labelledby="capabilities-heading">
        <h3 id="capabilities-heading">Provided capabilities <span class="count">{capabilities.length}</span></h3>
        {#each capabilities as item}
          <details>
            <summary>{item.capability.id} <span class="muted">· {item.snapshotOnly ? 'binding snapshot · ' : ''}{item.capability.ready ? 'ready' : 'not ready'}</span></summary>
            <p>{item.capability.interface || 'No interface advertised'} · version {item.capability.version || 'not advertised'}</p>
            <button type="button" on:click={() => inspect('Capability', { identity: item.node.manifest.identity, capability: item.capability, snapshot_only: item.snapshotOnly })}>Inspect capability JSON</button>
          </details>
        {:else}<p class="empty">No capabilities in this node snapshot.</p>{/each}
      </section>

      <section class="relation-section" aria-labelledby="dependencies-heading">
        <div class="section-heading">
          <h3 id="dependencies-heading">Declared dependencies <span class="count">{requirements.length}</span></h3>
          <label class="filter">Show dependencies<select bind:value={dependencyFilter}><option value="all">Required and optional</option><option value="required">Required only</option><option value="optional">Optional only</option></select></label>
        </div>
        <p>Declaration candidates match advertised ID/interface only. They do not establish eligibility or bindings. Remote availability stays unknown.</p>
        {#each visibleRequirements as item}
          <details>
            <summary><span class:warning={item.optional}>{item.optional ? 'Optional' : 'Required'}</span> · {item.declaration.id} <span class:unavailable={item.unavailable || item.cycle} class="muted">· {item.status}</span></summary>
            <p>Requests {item.declaration.capability || item.declaration.interface || 'any advertised capability'}.</p>
            <button type="button" on:click={() => inspect('Requirement declaration and local explanation', item)}>Inspect requirement JSON</button>
            <details class="nested">
              <summary>Declared candidates · {item.candidates.length} · not bindings</summary>
              <p>Trust, version, readiness, scopes, locality and remote resolution are not inferred.</p>
              <ul class="relation-list">
                {#each item.candidates as candidate}
                  <li><strong>{candidate.capability.id}</strong><span>{candidate.node.manifest.identity.name} / {candidate.node.manifest.identity.instance_id}</span><div class="actions"><button type="button" on:click={() => focusNode(candidate.node.key)}>Focus provider node</button><button type="button" on:click={() => inspect('Declared candidate — not a binding or eligibility claim', { requirement: item.declaration, provider: candidate.capability, identity: candidate.node.manifest.identity })}>Inspect candidate JSON</button></div></li>
                {:else}<li>No advertised ID/interface match in this snapshot.</li>{/each}
              </ul>
            </details>
            {#if item.node.local}<p>Full next-selection evidence, provider rejections and cycles appear under Local next-selection explanations below.</p>{/if}
          </details>
        {:else}<p class="empty">{requirements.length ? 'No dependencies match this display filter.' : focusedNode.snapshotOnly ? 'Historical binding snapshots do not establish current dependency declarations.' : 'No dependencies declared by this node.'}</p>{/each}
      </section>

      <section class="relation-section" aria-labelledby="consumers-heading">
        <h3 id="consumers-heading">Incoming declaration candidates <span class="count">{incomingDeclarations.length}</span></h3>
        <details>
          <summary>Nodes declaring matching dependencies · not recorded consumers</summary>
          <ul class="relation-list">
            {#each incomingDeclarations as edge}
              <li><strong>{edge.source.node.manifest.identity.name} · {edge.source.declaration.id}</strong><span>{edge.source.optional ? 'Optional' : 'Required'} declaration matches {edge.target.capability.id}. Eligibility and remote bindings are not inferred.</span><div class="actions"><button type="button" on:click={() => focusNode(edge.source.node.key)}>Focus declaring node</button><button type="button" on:click={() => inspect('Declared candidate — not a binding or eligibility claim', { requirement: edge.source.declaration, provider: edge.target.capability, identity: edge.target.node.manifest.identity })}>Inspect candidate JSON</button></div></li>
            {:else}<li>No declarations match this node's advertised capabilities.</li>{/each}
          </ul>
        </details>
      </section>

      <section class="relation-section" aria-labelledby="recorded-heading">
        <h3 id="recorded-heading">Recorded local bindings <span class="count recorded">{recorded.length}</span></h3>
        <p>Prior local selections involving this node. Records do not guarantee current availability. No remote bindings are inferred.</p>
        {#each recorded as edge}
          <details>
            <summary><span class="recorded">Recorded</span> · {edge.requirementKey || 'Unattributed legacy record'} · {edge.binding.capability.id}</summary>
            <p>Local requirement to {edge.binding.node.identity.name} / {edge.binding.node.identity.instance_id}</p>
            {#if edge.binding.reason}<p>{edge.binding.reason}</p>{/if}
            <div class="actions"><button type="button" on:click={() => focusNode(edge.target.node.key)}>Focus provider node</button><button type="button" on:click={() => inspect('Recorded local binding — historical selection, not current availability', edge)}>Inspect relationship JSON</button></div>
            <pre>{JSON.stringify(edge.binding, null, 2)}</pre>
          </details>
        {:else}<p class="empty">No recorded local bindings involve this node.</p>{/each}
      </section>

      <section class="relation-section" aria-labelledby="preview-heading">
        <h3 id="preview-heading">Next-selection preview <span class="count preview">{previews.length}</span></h3>
        <p>Read-only local resolver preview involving this node, not recorded bindings. No schedule, sticky pin or recorded binding changes.</p>
        {#each previews as edge}
          <details>
            <summary><span class="preview">Preview only</span> · {edge.requirementKey} · {edge.binding.capability.id}</summary>
            <p>Local requirement to {edge.binding.node.identity.name} / {edge.binding.node.identity.instance_id}</p>
            <div class="actions"><button type="button" on:click={() => focusNode(edge.target.node.key)}>Focus provider node</button><button type="button" on:click={() => inspect('Next-selection preview — not a recorded binding', edge)}>Inspect preview JSON</button></div>
          </details>
        {:else}<p class="empty">No next-selection preview targets involve this node. Check local explanations for unavailable requirements.</p>{/each}
      </section>

      <section class="relation-section" aria-labelledby="routes-heading">
        <h3 id="routes-heading">Advertised relay routes <span class="count">{routes.length}</span></h3>
        <p>Transport advertisements only, not proof of a connected tunnel or live traffic. A relay capability alone creates no route.</p>
        {#each routes as edge}
          <details>
            <summary>{edge.source.manifest.identity.name} · {edge.address}</summary>
            <p>Advertised endpoint: {edge.base}</p>
            <div class="actions"><button type="button" on:click={() => focusNode(edge.source.key)}>Focus advertising node</button><button type="button" on:click={() => inspect('Advertised relay route — no live connection claim', edge)}>Inspect route JSON</button>
              {#if edge.target.manifest}<button type="button" on:click={() => focusNode(edge.target.key)}>Focus advertised relay node</button>{:else}<button type="button" on:click={() => inspect('Advertised relay endpoint — hub identity unknown', edge.target)}>Inspect endpoint JSON</button>{/if}
            </div>
            {#if !edge.target.manifest}<p>Hub identity unknown; no unique current node matches this endpoint.</p>{/if}
          </details>
        {:else}<p class="empty">No advertised relay routes involve this node.</p>{/each}
      </section>
    {:else}<p class="empty">No node manifests in this snapshot. Refresh to load available nodes.</p>{/if}
  </section>

  <section class="panel" aria-labelledby="inspection-heading" tabindex="-1" bind:this={inspection}>
    <div class="section-heading"><h3 id="inspection-heading">Selection details</h3>{#if selected}<button type="button" on:click={() => selected = null}>Clear inspection</button>{/if}</div>
    {#if selected}<h4>{selected.kind}</h4><pre>{JSON.stringify(selected.data, null, 2)}</pre>{:else}<p>Select any Inspect JSON button to see full identities, declarations, transport, operations and scopes. All controls support keyboard navigation.</p>{/if}
  </section>

  <section class="panel">
    <h3>Local next-selection explanations</h3><p>All local requirements, independent of the node and display filter above. Read-only previews, not recorded selections. Rejected evidence can include nested required dependencies and cycles.</p>
    {#each localRequirements as item}
      <details>
        <summary>{item.declaration.id} · {item.optional ? 'optional' : 'required'} · <span class:unavailable={item.unavailable || item.cycle}>{item.status}</span></summary>
        {#if item.explanation}
          <p>Effective policy: <strong>{item.explanation.policy || item.explanation.requirement?.policy || 'not reported'}</strong>. Sticky: {item.explanation.sticky ? 'yes' : 'no'}{item.explanation.sticky_pin ? ` · saved boundary: ${item.explanation.sticky_pin}` : ''}.</p>
          <p>{item.explanation.rationale}</p>
          {#if item.explanation.error}<p class="error">{item.explanation.error}</p>{/if}
          <p>{(item.explanation.selected || []).length} preview selection(s), {(item.explanation.providers || []).filter(provider => provider.eligible).length} eligible provider(s), {(item.explanation.providers || []).filter(provider => !provider.eligible).length} rejected provider(s).</p>
          {#each item.explanation.providers || [] as provider}
            <details class="nested"><summary>{provider.node.name} / {provider.capability.id} · {provider.selected ? 'preview selected' : provider.eligible ? 'eligible, not selected' : 'rejected'}{provider.rank ? ` · rank ${provider.rank}` : ''}</summary>
              <p>{provider.node.namespace} / {provider.node.principal} / {provider.node.instance_id}</p>
              <ul>{#each providerReasons([provider]) as reason}<li><strong>{reason.code}</strong>: {reason.message}{reason.requirement_key ? ` (requirement ${reason.requirement_key})` : ''}</li>{/each}</ul>
              <pre>{JSON.stringify(provider, null, 2)}</pre>
            </details>
          {/each}
          <details class="nested"><summary>Full explanation JSON</summary><pre>{JSON.stringify(item.explanation, null, 2)}</pre></details>
        {:else}<p>No local explanation in this snapshot. Availability unknown.</p>{/if}
      </details>
    {:else}<p class="empty">No local requirements declared.</p>{/each}
  </section>

  <section class="panel">
    <h3>Local resolver policy</h3><p>Only an explicit save changes this node's declared requirement policy/pin and clears its saved sticky boundary. It does not select a provider or edit peer policies, trust grants or recorded bindings. Available policies come from this snapshot's endpoint.</p>
    <form on:submit|preventDefault={savePolicy}>
      <div class="policy-fields">
        <label>Requirement<select bind:value={requirement} required><option value="">Choose requirement</option>{#each localRequirements as item}<option value={item.declaration.id}>{item.declaration.id}</option>{/each}</select></label>
        <label>Policy<select bind:value={policy} required disabled={!policies.length}><option value="">Choose policy</option>{#each policies as value}<option value={value}>{value}</option>{/each}</select></label>
        <label>Pin name / identity<input bind:value={pin} /></label>
      </div>
      <button disabled={saving || !requirement || !policy || !policies.includes(policy)}>{saving ? 'Saving…' : 'Save policy (no selection)'}</button>
    </form>
    {#if !policies.length}<p>No policy list supplied. Policy editing unavailable.</p>{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </section>
</div>

<style>
  .topology-browser { display: grid; gap: 1.5rem; min-width: 0; }
  .topology-browser :is(section, div, form, label, details) { min-width: 0; }
  .panel { margin: 0; overflow-wrap: anywhere; }
  .section-heading, .node-heading { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 1rem; }
  .section-heading > div, .node-heading > div { flex: 1 1 18rem; }
  h2, h3, h4 { margin: 0; }
  h4 { margin-top: 1rem; }
  p { max-width: 75ch; margin: .75rem 0; color: var(--color-text-muted); }
  label { display: grid; gap: .5rem; }
  .node-picker { margin-top: 1.5rem; }
  .node-heading { margin-top: 1.5rem; }
  .identity { margin-bottom: 0; }
  .node-state { margin-top: .75rem; }
  .relation-section { margin-top: 1.75rem; padding-top: 1.5rem; border-top: 1px solid var(--color-base-300); }
  .count { margin-left: .4rem; color: var(--color-text-muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .filter { font-size: var(--text-ui); flex: 0 1 16rem; }
  select, input { box-sizing: border-box; width: 100%; min-width: 0; max-width: 100%; }
  button { max-width: 100%; white-space: normal; overflow-wrap: anywhere; }
  button, select, input { min-height: 2.75rem; border-radius: 8px; }
  details { border-bottom: 1px solid var(--color-base-300); padding: .875rem 0; }
  summary { cursor: pointer; line-height: 1.6; color: var(--color-base-content); overflow-wrap: anywhere; }
  details[open] > summary { margin-bottom: .75rem; }
  .nested { margin-top: .75rem; padding: .75rem; background: var(--color-base-200); border-radius: 8px; border: 1px solid var(--color-base-300); }
  .relation-list { list-style: none; margin: 0; padding: 0; }
  .relation-list li { display: grid; gap: .5rem; padding: 1rem 0; border-bottom: 1px solid var(--color-base-300); }
  .relation-list li:last-child { border-bottom: 0; }
  .relation-list li > span, .muted { color: var(--color-text-muted); }
  .actions { display: flex; flex-wrap: wrap; gap: .75rem; margin-top: .5rem; }
  .recorded { color: var(--color-success); }
  .preview { color: var(--color-info); }
  .warning { color: var(--color-warning); }
  .error, .unavailable { color: var(--color-error); }
  .empty { padding: .5rem 0; }
  pre { box-sizing: border-box; max-width: 100%; max-height: 32rem; overflow-y: auto; overflow-x: hidden; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; line-height: 1.6; background: var(--color-base-200); border-radius: 8px; padding: 1rem; tab-size: 2; }
  .policy-fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 14rem), 1fr)); gap: 1rem; margin: 1.25rem 0; }
  :is(button, select, input, summary):focus-visible { outline: 2px solid var(--color-primary); outline-offset: 3px; }
  ::selection { color: var(--color-primary-content); background: var(--color-primary); }
  @media (max-width: 40rem) {
    .section-heading, .node-heading { align-items: stretch; }
    .section-heading > button, .node-heading > button, .filter { width: 100%; flex-basis: 100%; }
    .actions { flex-direction: column; align-items: stretch; }
    .nested { padding: .75rem .5rem; }
  }
</style>
