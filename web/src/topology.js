const identityKey = identity => JSON.stringify([identity?.namespace, identity?.principal, identity?.name, identity?.instance_id]);
const capabilityKey = (identity, capability) => JSON.stringify([identityKey(identity), capability.id]);

export function providerReasons(providers = []) {
  return providers.flatMap(provider => (provider.reasons || []).flatMap(reason => [reason, ...providerReasons(reason.providers || [])]));
}

// Only transport advertisements establish a route. A capability declaration does not.
function relayRoute(address, identity) {
  try {
    const url = new URL(address);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) return null;
    const match = url.pathname.match(/^(.*)\/relay\/forward\/([^/]+)\/([^/]+)$/);
    if (!match || decodeURIComponent(match[2]) !== identity.principal || decodeURIComponent(match[3]) !== identity.instance_id) return null;
    return { address, base: url.origin + match[1] };
  } catch { return null; }
}

function endpointKey(address) {
  try {
    const url = new URL(address);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) return null;
    return (url.origin + url.pathname).replace(/\/+$/, '');
  } catch { return null; }
}

export function buildTopology(topology = {}) {
  topology ||= {};
  const nodes = [];
  const capabilities = [];
  const requirements = [];
  const declarations = [];
  const recorded = [];
  const previews = [];
  const routes = [];
  const endpoints = [];
  const nodeMap = new Map();
  const capabilityMap = new Map();
  const localKey = topology.manifest && identityKey(topology.manifest.identity);

  function addNode(manifest, snapshotOnly = false) {
    if (!manifest?.identity) return null;
    const key = identityKey(manifest.identity);
    if (!nodeMap.has(key)) {
      const node = { key, manifest, snapshotOnly, local: key === localKey, x: 155 + nodes.length * 320, y: 85 };
      nodeMap.set(key, node);
      nodes.push(node);
      for (const capability of manifest.provides || []) addCapability(node, capability, snapshotOnly);
    }
    return nodeMap.get(key);
  }

  function addCapability(node, capability, snapshotOnly) {
    const key = capabilityKey(node.manifest.identity, capability);
    if (!capabilityMap.has(key)) {
      const index = capabilities.filter(item => item.node === node).length;
      const item = { key, node, capability, snapshotOnly, x: node.x, y: 185 + index * 64 };
      capabilities.push(item);
      capabilityMap.set(key, item);
    }
    return capabilityMap.get(key);
  }

  function bindingTarget(binding) {
    const node = addNode(binding.node, true);
    return node && binding.capability ? addCapability(node, binding.capability, true) : null;
  }

  addNode(topology.manifest);
  for (const peer of topology.peers || []) addNode(peer.manifest);
  const currentNodes = [...nodes];
  const groups = Array.isArray(topology.recorded_bindings)
    ? topology.recorded_bindings
    : [{ requirement_key: null, bindings: topology.bindings || [] }];

  // Include historical targets without presenting their declarations as current peer evidence.
  for (const group of groups) {
    for (const binding of group.bindings || []) {
      const target = bindingTarget(binding);
      if (target) recorded.push({ requirementKey: group.requirement_key, binding, target });
    }
  }
  for (const [id, explanation] of Object.entries(topology.resolutions || {})) {
    for (const binding of explanation.selected || []) {
      const target = bindingTarget(binding);
      if (target) previews.push({ requirementKey: explanation.requirement_key || id, binding, target });
    }
  }

  for (const node of currentNodes) {
    const capabilityCount = capabilities.filter(item => item.node === node).length;
    for (const [index, declaration] of (node.manifest.requires || []).entries()) {
      const explanation = node.local ? topology.resolutions?.[declaration.id] : null;
      const requirement = explanation?.requirement || declaration;
      const candidates = capabilities.filter(item => !item.snapshotOnly &&
        (!declaration.capability || item.capability.id === declaration.capability) &&
        (!declaration.interface || item.capability.interface === declaration.interface));
      const reasons = providerReasons(explanation?.providers || []);
      const cycle = reasons.some(reason => reason.code === 'required_cycle');
      const unavailable = !!explanation && (!!explanation.error || !(explanation.selected || []).length);
      let status = node.local
        ? explanation ? unavailable ? 'Unavailable for next selection' : 'Next-selection preview available' : 'Local explanation missing'
        : candidates.length ? 'Declared candidates only; availability unknown' : 'No declared candidate; availability unknown';
      if (cycle) status += ' · cycle evidence';
      const item = {
        key: JSON.stringify([node.key, declaration.id, index]), node, declaration, requirement, explanation,
        requirementKey: explanation?.requirement_key || declaration.id,
        optional: !!declaration.optional, candidates, cycle, unavailable, status,
        x: node.x, y: 210 + capabilityCount * 64 + index * 84,
      };
      requirements.push(item);
      for (const target of candidates) declarations.push({ source: item, target });
    }
  }
  const local = nodeMap.get(localKey);
  for (const edge of [...recorded, ...previews]) {
    edge.source = requirements.find(item => item.node.local && item.requirementKey === edge.requirementKey) || local;
  }

  let bottom = Math.max(320, ...capabilities.map(item => item.y + 50), ...requirements.map(item => item.y + 55));
  for (const node of currentNodes) {
    for (const address of node.manifest.transport?.advertise || []) {
      const route = relayRoute(address, node.manifest.identity);
      if (!route) continue;
      const hubs = currentNodes.filter(hub => hub !== node && (hub.manifest.transport?.advertise || []).some(value => endpointKey(value) === route.base));
      let target = hubs.length === 1 ? hubs[0] : endpoints.find(item => item.base === route.base);
      if (!target) {
        target = { base: route.base, x: 155 + endpoints.length * 320, y: bottom + 85 };
        endpoints.push(target);
      }
      routes.push({ source: node, target, ...route });
    }
  }
  return {
    nodes, capabilities, requirements, declarations, recorded: recorded.filter(edge => edge.source), previews: previews.filter(edge => edge.source), routes, endpoints,
    width: Math.max(960, nodes.length * 320, endpoints.length * 320),
    height: bottom + (endpoints.length ? 160 : 60),
  };
}
