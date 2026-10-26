<script>
  import { nodeID } from './api.js';
  import Models from './Models.svelte';
  import Adapters from './Adapters.svelte';
  import Resource from './Resource.svelte';
  export let topology;
  export let providers = [];
  export let refresh;
  let selected = '';
  let query = '';
  $: nodes = [...new Map([topology?.manifest, ...(topology?.peers || []).map(peer => peer.manifest)].filter(Boolean).map(node => [nodeID(node), node])).values()];
  $: visible = nodes.filter(node => [node.identity.name, node.identity.namespace, ...(node.provides || []).map(capability => capability.id)].join(' ').toLowerCase().includes(query.toLowerCase()));
  $: node = nodes.find(item => nodeID(item) === selected);
  $: nodeProviders = providers.filter(item => nodeID(item.node) === selected);
  $: modelSettings = nodeProviders.some(item => item.capability.operations?.some(operation => operation.path === '/llm/config' && operation.method === 'GET'));
  $: adapterSettings = nodeProviders.some(item => item.capability.id === 'adapter.telegram');
  const hasSettings = node => (node.provides || []).some(capability => capability.id === 'adapter.telegram' || capability.operations?.some(operation => operation.path === '/llm/config' && operation.method === 'GET'));
</script>

{#if selected && node}
  <div class="row spread node-toolbar"><button on:click={() => selected = ''}>Back to nodes</button><button on:click={refresh}>Refresh snapshot</button></div>
  <section class="panel"><div class="row spread"><h2>{node.identity.name}</h2><span class="badge">{node.lifecycle?.ready ? 'Ready' : 'Not ready'}</span></div><p>{node.identity.namespace} · {node.provides?.length || 0} advertised capabilities</p>
    <details><summary>Node identity and manifest</summary><pre>{JSON.stringify(node, null, 2)}</pre></details>
  </section>
  {#key selected}
    {#if modelSettings}<Models providers={nodeProviders} />{/if}
    {#if adapterSettings}<Adapters providers={nodeProviders} />{/if}
    {#if !modelSettings && !adapterSettings}<p class="hint">This node does not advertise a dedicated settings editor. Inspect its available operations below.</p>{/if}
    <Resource title="Node operations" description="Operations advertised by this exact node. Your user scopes still control access." providers={nodeProviders} />
  {/key}
{:else}
  {#if selected}<p class="notice" role="status">The selected node is no longer in the current snapshot.</p>{/if}
  <section class="panel node-intro"><div><h2>Your mesh, node by node</h2><p>Open a node to inspect its capabilities and available settings.</p></div><button on:click={refresh}>Refresh snapshot</button></section>
  <div class="row spread node-toolbar"><label class="node-search">Find a node<input type="search" bind:value={query} placeholder="Name, namespace or capability" /></label><p role="status">{visible.length} of {nodes.length} nodes</p></div>
  <div class="node-grid">
    {#each visible as item (nodeID(item))}
      <button class="node-tile" on:click={() => selected = nodeID(item)}>
        <span class="row spread"><strong>{item.identity.name}</strong><span class:ready={item.lifecycle?.ready} class="node-status">{item.lifecycle?.ready ? 'Ready' : 'Not ready'}</span></span>
        <span class="node-namespace">{item.identity.namespace}{item === topology?.manifest ? ' · local node' : ''}</span>
        <span class="node-capabilities">{#each (item.provides || []).slice(0, 5) as capability}<span>{capability.id}</span>{:else}<span>No advertised capabilities</span>{/each}{#if item.provides?.length > 5}<span>+{item.provides.length - 5} more</span>{/if}</span>
        <span class="node-tile-footer"><span>{item.provides?.length || 0} capabilities · {item.requires?.length || 0} requirements</span><span>{hasSettings(item) ? 'Open settings' : 'Inspect node'}</span></span>
      </button>
    {:else}<section class="panel"><h3>{nodes.length ? 'No matching nodes' : 'No nodes available'}</h3><p>{nodes.length ? 'Try another name or capability.' : 'Refresh the snapshot to check mesh membership.'}</p></section>{/each}
  </div>
{/if}

<style>
  .node-intro{display:flex;align-items:center;justify-content:space-between;gap:1.5rem}
  .node-intro p{margin-bottom:0}
  .node-toolbar{margin-bottom:1.5rem}
  .node-search{max-width:30rem}
  .node-toolbar p{font-size:var(--text-ui)}
  .node-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,19rem),1fr));gap:1.25rem}
  .node-tile{display:flex;flex-direction:column;text-align:left;gap:1rem;padding:1.5rem;background:var(--color-base-100);border:1px solid var(--color-base-300);min-width:0;overflow-wrap:anywhere;font-weight:400;line-height:1.6}
  .node-tile:hover{border-color:var(--color-primary)}
  .node-tile strong{font-size:var(--text-heading);letter-spacing:-.015em}
  .node-status{font-size:var(--text-meta);font-weight:500;color:var(--color-warning)}
  .node-status.ready{color:var(--color-success)}
  .node-namespace{font-size:var(--text-ui);color:var(--color-text-muted)}
  .node-capabilities{display:flex;flex-wrap:wrap;gap:.5rem;flex:1;align-content:start}
  .node-capabilities>span{font-size:var(--text-meta);background:var(--color-base-200);padding:.375rem .5rem;border-radius:4px}
  .node-tile-footer{display:flex;flex-wrap:wrap;justify-content:space-between;gap:.75rem;border-top:1px solid var(--color-base-300);padding-top:1rem;font-size:var(--text-meta);color:var(--color-text-muted);font-variant-numeric:tabular-nums}
  .node-tile-footer>span:last-child{color:var(--color-primary);font-weight:500}
  @media(max-width:650px){.node-intro{align-items:start;flex-direction:column}.node-search{max-width:none}.node-tile{padding:1.25rem}}
</style>
