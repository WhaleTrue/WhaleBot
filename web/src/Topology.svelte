<script>
  import { json } from './api.js';
  import { buildTopology, providerReasons } from './topology.js';
  export let topology;
  export let refresh;
  let selected = null;
  let selectedTopology;
  let error = '';
  let saving = false;
  let requirement = '';
  let editedRequirement = '';
  let policy = '';
  let pin = '';
  $: graph = buildTopology(topology);
  $: policies = topology?.policies || [];
  $: localRequirements = graph.requirements.filter(item => item.node.local);
  $: if (topology !== selectedTopology) { selectedTopology = topology; selected = null; }
  $: if (requirement !== editedRequirement) {
    editedRequirement = requirement;
    const explanation = topology?.resolutions?.[requirement];
    const effective = explanation?.policy || explanation?.requirement?.policy;
    policy = policies.includes(effective) ? effective : '';
    pin = explanation?.requirement?.pin || '';
  }
  const clip = (value, length = 32) => value?.length > length ? value.slice(0, length - 1) + '…' : value || '';
  const inspect = (kind, data) => selected = { kind, data };
  function activate(event, kind, data) {
    if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); inspect(kind, data); }
  }
  function path(edge, offset = 0) {
    return `M ${edge.source.x + 125} ${edge.source.y + offset} C ${edge.source.x + 230 + offset} ${edge.source.y + offset} ${edge.target.x + 230 + offset} ${edge.target.y + offset} ${edge.target.x + 125} ${edge.target.y + offset}`;
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

<section class="panel">
  <div class="row spread"><div><h2>Live capability topology</h2><p>Read-only snapshot. Opening, inspecting and refreshing never resolve or select providers.</p></div><button on:click={refresh}>Refresh snapshot</button></div>
  <ul class="topology-legend" aria-label="Graph legend">
    <li><span class="sample ownership"></span>Capability ownership</li>
    <li><span class="sample required"></span>Required declaration / candidates</li>
    <li><span class="sample optional"></span>Optional declaration / candidates</li>
    <li><span class="sample recorded"></span>Recorded local binding</li>
    <li><span class="sample preview"></span>Next-selection preview (not recorded)</li>
    <li><span class="sample route"></span>Advertised relay route (not live traffic)</li>
    <li><span class="unavailable">!</span>Local unavailable / cycle evidence</li>
    <li>Remote dependency availability: unknown</li>
  </ul>
  <div class="graph-scroll">
    <svg viewBox={`0 0 ${graph.width} ${graph.height}`} width={graph.width} height={graph.height} role="group" aria-label="Nodes, capabilities, declarations, recorded local bindings, previews and advertised relay routes">
      {#each graph.capabilities as item}
        <line class="ownership" x1={item.node.x} y1={item.node.y + 32} x2={item.x} y2={item.y} />
      {/each}
      {#each graph.requirements as item}
        <line class:optional={item.optional} class:required={!item.optional} x1={item.node.x - 125} y1={item.node.y + 32} x2={item.x - 125} y2={item.y} />
      {/each}
      {#each graph.declarations as edge}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Declared ${edge.source.optional ? 'optional' : 'required'} candidate for ${edge.source.declaration.id}`} on:click={() => inspect('Declared candidate — not a binding or eligibility claim', { requirement: edge.source.declaration, provider: edge.target.capability, identity: edge.target.node.manifest.identity })} on:keydown={event => activate(event, 'Declared candidate — not a binding or eligibility claim', { requirement: edge.source.declaration, provider: edge.target.capability, identity: edge.target.node.manifest.identity })}>
          <path d={path(edge)} class:optional={edge.source.optional} class:required={!edge.source.optional} class="candidate" /><title>Declaration matches capability ID/interface only. Trust, version, readiness, scopes, locality and remote resolution are not inferred.</title>
        </g>
      {/each}
      {#each graph.routes as edge}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Advertised relay route ${edge.address}`} on:click={() => inspect('Advertised relay route — no live connection claim', edge)} on:keydown={event => activate(event, 'Advertised relay route — no live connection claim', edge)}>
          <path d={path(edge, -12)} class="route" /><title>{edge.address}: advertised route to this node, not proof of a connected tunnel or traffic.</title>
        </g>
      {/each}
      {#each graph.recorded as edge}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Recorded local binding ${edge.requirementKey || 'unattributed'} to ${edge.binding.capability.id}`} on:click={() => inspect('Recorded local binding — historical selection, not current availability', edge)} on:keydown={event => activate(event, 'Recorded local binding — historical selection, not current availability', edge)}>
          <path d={path(edge, -6)} class="recorded" /><title>Recorded local binding: {edge.requirementKey || 'requirement attribution unavailable'}. {edge.binding.reason}</title>
        </g>
      {/each}
      {#each graph.previews as edge}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Next-selection preview ${edge.requirementKey} to ${edge.binding.capability.id}`} on:click={() => inspect('Next-selection preview — not a recorded binding', edge)} on:keydown={event => activate(event, 'Next-selection preview — not a recorded binding', edge)}>
          <path d={path(edge, 10)} class="preview" /><title>Read-only next-selection preview: {edge.requirementKey}. No schedule, sticky pin or recorded binding changed.</title>
        </g>
      {/each}
      {#each graph.nodes as node}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Node ${node.manifest.identity.name}, ${node.manifest.identity.instance_id}`} on:click={() => inspect(node.snapshotOnly ? 'Recorded/preview snapshot node — not a current peer' : 'Current node manifest', node.manifest)} on:keydown={event => activate(event, node.snapshotOnly ? 'Recorded/preview snapshot node — not a current peer' : 'Current node manifest', node.manifest)}>
          <rect x={node.x - 125} y={node.y - 32} width="250" height="64" rx="12" fill="#1b2b43" stroke={node.manifest.lifecycle?.ready ? '#5ddbb5' : '#fbbf24'} />
          <text x={node.x} y={node.y - 8} text-anchor="middle" fill="#edf3ff">{clip(node.manifest.identity.name, 29)}</text>
          <text x={node.x} y={node.y + 11} text-anchor="middle" fill="#a9bad4" font-size="11">{node.local ? 'local · ' : ''}{node.snapshotOnly ? 'binding snapshot only' : node.manifest.lifecycle?.ready ? 'ready' : 'not ready'} · {clip(node.manifest.identity.instance_id, 16)}</text>
          <title>{node.manifest.identity.namespace} / {node.manifest.identity.principal} / {node.manifest.identity.instance_id}</title>
        </g>
      {/each}
      {#each graph.capabilities as item}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Capability ${item.capability.id} on ${item.node.manifest.identity.name}`} on:click={() => inspect('Capability', { identity: item.node.manifest.identity, capability: item.capability, snapshot_only: item.snapshotOnly })} on:keydown={event => activate(event, 'Capability', { identity: item.node.manifest.identity, capability: item.capability, snapshot_only: item.snapshotOnly })}>
          <rect x={item.x - 125} y={item.y - 22} width="250" height="44" rx="7" fill="#172239" stroke={item.capability.ready ? '#536786' : '#fbbf24'} />
          <text x={item.x} y={item.y - 2} text-anchor="middle" fill="#dce7fc" font-size="12">{clip(item.capability.id)}</text>
          <text x={item.x} y={item.y + 14} text-anchor="middle" fill="#a9bad4" font-size="10">{item.snapshotOnly ? 'binding snapshot · ' : ''}{item.capability.ready ? 'capability ready' : 'capability not ready'}</text>
          <title>{item.capability.id} · {item.capability.interface} · {item.capability.version}</title>
        </g>
      {/each}
      {#each graph.requirements as item}
        <g class="svg-select" role="button" tabindex="0" aria-label={`${item.optional ? 'Optional' : 'Required'} requirement ${item.declaration.id}: ${item.status}`} on:click={() => inspect('Requirement declaration and local explanation', item)} on:keydown={event => activate(event, 'Requirement declaration and local explanation', item)}>
          <rect x={item.x - 125} y={item.y - 30} width="250" height="60" rx="3" fill="#211f34" stroke={item.unavailable || item.cycle ? '#ff9c9c' : item.optional ? '#fbbf24' : '#bda4ff'} stroke-dasharray={item.optional ? '3 4' : 'none'} />
          <text x={item.x} y={item.y - 12} text-anchor="middle" fill="#edf3ff" font-size="12">{item.optional ? 'Optional' : 'Required'}: {clip(item.declaration.id, 23)}</text>
          <text x={item.x} y={item.y + 4} text-anchor="middle" fill="#a9bad4" font-size="10">{clip(item.declaration.capability || item.declaration.interface)}</text>
          <text x={item.x} y={item.y + 20} text-anchor="middle" fill={item.unavailable || item.cycle ? '#ff9c9c' : '#dce7fc'} font-size="10">{item.unavailable ? '! Unavailable' : item.node.local ? item.explanation ? 'Preview available' : '? Explanation missing' : '? Availability unknown'}{item.cycle ? ' · cycle evidence' : ''}</text>
          <title>{item.status}</title>
        </g>
      {/each}
      {#each graph.endpoints as endpoint}
        <g class="svg-select" role="button" tabindex="0" aria-label={`Advertised relay endpoint ${endpoint.base}`} on:click={() => inspect('Advertised relay endpoint — hub identity unknown', endpoint)} on:keydown={event => activate(event, 'Advertised relay endpoint — hub identity unknown', endpoint)}>
          <rect x={endpoint.x - 125} y={endpoint.y - 25} width="250" height="50" rx="8" fill="#292236" stroke="#f5a5ce" />
          <text x={endpoint.x} y={endpoint.y - 5} text-anchor="middle" fill="#edf3ff" font-size="11">{clip(endpoint.base, 34)}</text>
          <text x={endpoint.x} y={endpoint.y + 14} text-anchor="middle" fill="#a9bad4" font-size="10">Relay endpoint · hub identity unknown</text>
          <title>{endpoint.base}</title>
        </g>
      {/each}
    </svg>
  </div>
  <p class="hint">Declaration candidates match advertised ID/interface only, not resolver eligibility. Only local explanations establish availability or cycle evidence. Remote declarations never establish remote bindings. Green recorded edges can remain while a cyan next-selection preview differs or fails. Relay edges require an advertised transport route; a relay capability alone changes no node color and creates no edge.</p>
</section>

<div class="columns">
  <section class="panel"><h3>Selection details</h3>{#if selected}<h4>{selected.kind}</h4><pre>{JSON.stringify(selected.data, null, 2)}</pre>{:else}<p>Select a node, capability, requirement or edge. Keyboard: Tab, then Enter or Space. Details include full identities, declarations, transport, operations and scopes.</p>{/if}</section>
  <section class="panel"><h3>Local next-selection explanations</h3><p>Read-only previews, not new or recorded selections. Rejected evidence can include nested required dependencies and cycles.</p>
    {#each localRequirements as item}
      <details>
        <summary>{item.declaration.id} · {item.optional ? 'optional' : 'required'} · {item.status}</summary>
        {#if item.explanation}
          <p>Effective policy: <strong>{item.explanation.policy || item.explanation.requirement?.policy || 'not reported'}</strong>. Sticky: {item.explanation.sticky ? 'yes' : 'no'}{item.explanation.sticky_pin ? ` · saved boundary: ${item.explanation.sticky_pin}` : ''}.</p>
          <p>{item.explanation.rationale}</p>
          {#if item.explanation.error}<p class="error">{item.explanation.error}</p>{/if}
          <p>{(item.explanation.selected || []).length} preview selection(s), {(item.explanation.providers || []).filter(provider => provider.eligible).length} eligible provider(s), {(item.explanation.providers || []).filter(provider => !provider.eligible).length} rejected provider(s).</p>
          {#each item.explanation.providers || [] as provider}
            <details><summary>{provider.node.name} / {provider.capability.id} · {provider.selected ? 'preview selected' : provider.eligible ? 'eligible, not selected' : 'rejected'}{provider.rank ? ` · rank ${provider.rank}` : ''}</summary>
              <p>{provider.node.namespace} / {provider.node.principal} / {provider.node.instance_id}</p>
              <ul>{#each providerReasons([provider]) as reason}<li><strong>{reason.code}</strong>: {reason.message}{reason.requirement_key ? ` (requirement ${reason.requirement_key})` : ''}</li>{/each}</ul>
              <pre>{JSON.stringify(provider, null, 2)}</pre>
            </details>
          {/each}
          <details><summary>Full explanation JSON</summary><pre>{JSON.stringify(item.explanation, null, 2)}</pre></details>
        {:else}<p>No local explanation in this snapshot. Availability unknown.</p>{/if}
      </details>
    {:else}<p>No local requirements declared.</p>{/each}
    <h3>Recorded local bindings</h3><p>Records of prior local selections, not guarantees of present availability. No remote bindings are inferred.</p>
    {#each graph.recorded as edge}<details><summary>{edge.requirementKey || 'Unattributed legacy record'} · {edge.binding.capability.id} · {edge.binding.node.identity.name}</summary><pre>{JSON.stringify(edge.binding, null, 2)}</pre></details>{:else}<p>No local bindings recorded.</p>{/each}
  </section>
</div>

<section class="panel"><h3>Local resolver policy</h3><p>Explicit save changes only this node's declared requirement policy/pin and clears its saved sticky boundary. It does not select a provider or edit peer policies, trust grants or recorded bindings. Available policies come from this snapshot's endpoint.</p>
  <form on:submit|preventDefault={savePolicy}><div class="row">
    <label>Requirement<select bind:value={requirement} required><option value="">Choose requirement</option>{#each localRequirements as item}<option value={item.declaration.id}>{item.declaration.id}</option>{/each}</select></label>
    <label>Policy<select bind:value={policy} required disabled={!policies.length}><option value="">Choose policy</option>{#each policies as value}<option value={value}>{value}</option>{/each}</select></label>
    <label>Pin name / identity<input bind:value={pin} /></label>
    <button disabled={saving || !requirement || !policy || !policies.includes(policy)}>{saving ? 'Saving…' : 'Save policy (no selection)'}</button>
  </div></form>
  {#if !policies.length}<p>No policy list supplied. Policy editing unavailable.</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  .topology-legend { display: flex; flex-wrap: wrap; gap: .65rem 1.2rem; list-style: none; padding: 0; font-size: .82rem; }
  .topology-legend li { display: flex; align-items: center; gap: .4rem; }
  .sample { display: inline-block; width: 30px; border-top: 2px solid; }
  .sample.ownership { border-color: #536786; }
  .sample.required { border-color: #bda4ff; }
  .sample.optional { border-color: #fbbf24; border-top-style: dotted; }
  .sample.recorded { border-color: #5ddbb5; border-top-width: 4px; }
  .sample.preview { border-color: #7dd3fc; border-top-style: dashed; }
  .sample.route { border-color: #f5a5ce; border-top-style: dotted; }
  .unavailable { color: #ff9c9c; font-weight: bold; }
  svg .ownership { stroke: #536786; stroke-width: 1.5; }
  svg .required { stroke: #bda4ff; stroke-width: 1.5; }
  svg .optional { stroke: #fbbf24; stroke-width: 1.5; stroke-dasharray: 2 6; }
  svg path { fill: none; }
  svg .candidate.required { stroke-dasharray: 7 5; }
  svg .candidate { opacity: .55; }
  svg .recorded { stroke: #5ddbb5; stroke-width: 4; }
  svg .preview { stroke: #7dd3fc; stroke-width: 3; stroke-dasharray: 12 5; }
  svg .route { stroke: #f5a5ce; stroke-width: 2; stroke-dasharray: 3 7; }
  .svg-select { cursor: pointer; }
  .svg-select:focus { outline: 2px solid #edf3ff; outline-offset: 3px; }
  .svg-select:hover path, .svg-select:focus path { stroke-width: 6; opacity: 1; }
  pre { max-height: 32rem; overflow: auto; }
</style>
