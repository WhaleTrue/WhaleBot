import test from 'node:test';
import assert from 'node:assert/strict';
import { buildTopology, providerReasons } from './topology.js';

const capability = id => ({ id, interface: `example.${id}`, ready: true });
const manifest = (id, provides = [], requires = [], advertise = []) => ({
  identity: { namespace: 'example', principal: id, instance_id: id, name: id },
  provides: provides.map(capability), requires,
  lifecycle: { alive: true, ready: true }, transport: { advertise },
});
const binding = node => ({ node, capability: node.provides[0], reason: 'recorded reason' });

test('graph separates recorded bindings, next previews and remote declarations without mutating snapshot', () => {
  const required = { id: 'needed', capability: 'data' };
  const optional = { id: 'extra', capability: 'absent', optional: true };
  const local = manifest('browser', [], [required, optional]);
  const old = manifest('old-provider', ['data']);
  const next = manifest('next-provider', ['data'], [{ id: 'remote-needed', capability: 'data' }, optional]);
  const snapshot = {
    manifest: local, peers: [{ manifest: next }], bindings: [binding(old)],
    recorded_bindings: [{ requirement_key: 'needed', bindings: [binding(old)] }],
    resolutions: {
      needed: { requirement_key: 'needed', requirement: required, policy: 'round-robin', selected: [binding(next)], providers: [] },
      extra: { requirement_key: 'extra', requirement: optional, selected: [], providers: [] },
    },
  };
  const before = JSON.stringify(snapshot);
  const graph = buildTopology(snapshot);
  assert.equal(JSON.stringify(snapshot), before);
  assert.equal(graph.nodes.length, 3);
  assert.equal(graph.capabilities.length, 2);
  assert.equal(graph.recorded.length, 1);
  assert.equal(graph.previews.length, 1);
  assert.equal(graph.recorded[0].target.node.manifest.identity.instance_id, 'old-provider');
  assert.equal(graph.previews[0].target.node.manifest.identity.instance_id, 'next-provider');
  assert.equal(graph.recorded[0].source.declaration.id, 'needed');
  assert.equal(graph.previews[0].source, graph.recorded[0].source);
  const localNeeded = graph.requirements.find(item => item.node.local && item.declaration.id === 'needed');
  assert.equal(localNeeded.unavailable, false);
  assert.equal(localNeeded.optional, false);
  assert.equal(localNeeded.candidates.length, 1, 'historical manifests are not current declaration candidates');
  const localOptional = graph.requirements.find(item => item.node.local && item.optional);
  assert.equal(localOptional.unavailable, true, 'empty optional selection is still visibly unavailable');
  const remote = graph.requirements.filter(item => !item.node.local);
  assert.equal(remote.length, 2);
  assert.ok(remote.every(item => !item.explanation && !item.unavailable && !item.cycle));
  assert.match(remote[0].status, /Declared candidates only; availability unknown/);
  assert.match(remote[1].status, /No declared candidate; availability unknown/);
  assert.ok([...graph.recorded, ...graph.previews].every(edge => edge.source.node.local));
});

test('local rejection evidence marks nested cycles without inferring remote resolution', () => {
  const required = { id: 'needed', capability: 'data' };
  const reasons = [{ code: 'required_dependency', message: 'dependency unavailable', requirement_key: 'nested', providers: [{ reasons: [{ code: 'required_cycle', message: 'path revisits provider' }] }] }];
  const provider = manifest('provider', ['data'], [required]);
  const graph = buildTopology({
    manifest: manifest('browser', [], [required]), peers: [{ manifest: provider }],
    resolutions: { needed: { requirement_key: 'needed', requirement: required, error: 'unavailable', selected: [], providers: [{ node: provider.identity, capability: provider.provides[0], eligible: false, reasons }] } },
  });
  const local = graph.requirements.find(item => item.node.local);
  assert.equal(local.unavailable, true);
  assert.equal(local.cycle, true);
  assert.match(local.status, /cycle evidence/);
  assert.equal(graph.requirements.find(item => !item.node.local).cycle, false);
  assert.deepEqual(providerReasons(local.explanation.providers).map(reason => reason.code), ['required_dependency', 'required_cycle']);
  assert.equal(buildTopology({ manifest: manifest('browser', [], [required]) }).requirements[0].unavailable, false, 'missing explanation means unknown, not unavailable');
});

test('relay edges require identity-matching transport routes, not relay capabilities or names', () => {
  const hub = manifest('ordinary-name', [], [], ['https://relay.example/base/']);
  const capabilityOnly = manifest('relay-name', ['mesh.relay'], [], ['https://unused.example']);
  const routed = manifest('outbound', ['data'], [], [
    'https://relay.example/base/relay/forward/outbound/outbound',
    'https://external.example/relay/forward/outbound/outbound',
    'https://relay.example/base/relay/forward/other/outbound',
    'https://relay.example/base/relay/forward/outbound/wrong-instance',
    'https://relay.example/base/relay/forward/outbound/outbound?bad=1',
    'https://relay.example/base/relay/forward/%ZZ/outbound',
  ]);
  const graph = buildTopology({ manifest: routed, peers: [{ manifest: hub }, { manifest: capabilityOnly }] });
  assert.equal(graph.routes.length, 2);
  assert.equal(graph.routes[0].target.manifest.identity.instance_id, 'ordinary-name');
  assert.equal(graph.endpoints.length, 1);
  assert.equal(graph.routes[1].target.base, 'https://external.example');
  assert.ok(graph.routes.every(edge => edge.source.manifest.identity.instance_id === 'outbound'));
  assert.equal(buildTopology({ manifest: capabilityOnly }).routes.length, 0);
});

test('unattributed legacy bindings remain records, never assigned by matching capability', () => {
  const provider = manifest('provider', ['data']);
  const graph = buildTopology({ manifest: manifest('browser', [], [{ id: 'needed', capability: 'data' }]), peers: [{ manifest: provider }], bindings: [binding(provider)] });
  assert.equal(graph.recorded[0].source.manifest.identity.instance_id, 'browser');
  assert.equal(graph.recorded[0].requirementKey, null);
  assert.equal(graph.previews.length, 0);
  assert.equal(buildTopology(null).nodes.length, 0);
});
