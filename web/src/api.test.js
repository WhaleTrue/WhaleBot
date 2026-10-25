import test from 'node:test';
import assert from 'node:assert/strict';
import { consumeSSE, nodeID } from './api.js';
import { benchmarkHistory, benchmarkScore, benchmarkCSV } from './benchmark.js';

test('SSE preserves fragmented Unicode, event types and multiline data', async () => {
  const bytes = new TextEncoder().encode('event: delta\r\ndata: {"content":"你好"}\r\n\r\nevent: done\ndata: {"reply":\ndata: "ok"}\n\n');
  const response = new Response(new ReadableStream({ start(controller) { for (let i = 0; i < bytes.length; i += 3) controller.enqueue(bytes.slice(i, i + 3)); controller.close(); } }));
  const events = [];
  await consumeSSE(response, (event, data) => events.push({ event, data }));
  assert.deepEqual(events, [{ event: 'delta', data: { content: '你好' } }, { event: 'done', data: { reply: 'ok' } }]);
});

test('node identifiers keep URI components and principals distinct', () => {
  const node = identity => ({ identity: { namespace: 'mesh', ...identity } });
  assert.notEqual(nodeID(node({ principal: 'a', name: 'b', instance_id: 'c/d/e' })), nodeID(node({ principal: 'a/b', name: 'c', instance_id: 'd/e' })));
  assert.notEqual(nodeID(node({ principal: 'a', name: 'same', instance_id: 'restart' })), nodeID(node({ principal: 'b', name: 'same', instance_id: 'restart' })));
});

test('benchmark history sorts newest first and never compares best across case sets', () => {
  const runs = [
    { id: 'old', started_at: '2026-01-01', case_set: 'mesh-v1', status: 'completed', scores: { total: 100 } },
    { id: 'new', started_at: '2026-03-01', case_set: 'mesh-v4.1', status: 'completed', scores: { total: 40 } },
    { id: 'better', started_at: '2026-02-01', case_set: 'mesh-v4.1', status: 'completed', scores: { total: 60 } },
    { id: 'failed', started_at: '2026-04-01', case_set: 'mesh-v4.1', status: 'failed', scores: { total: 99 } },
  ];
  const result = benchmarkHistory(runs);
  assert.equal(result.latestCaseSet, 'mesh-v4.1');
  assert.equal(result.bestId, 'better');
  assert.equal(result.runs[0].id, 'failed');
  assert.equal(runs[0].id, 'old');
  assert.equal(benchmarkHistory([{ id: 'pending', started_at: '2026-05-01', case_set: 'mesh-v5', status: 'running' }, ...runs]).bestId, '');
  assert.equal(benchmarkHistory([{ id: 'legacy', status: 'completed', scores: { total: 100 } }]).bestId, '');
});

test('benchmark stable columns preserve zero, omit incomplete scores and export safe CSV', () => {
  const completed = { id: 'run', model: '=FORMULA()', upstream_model: 'model,"quoted"', case_set: 'mesh-v4.1', status: 'completed', scores: { total: 0, bonus: 80, e2e: 50 }, usage: { total_tokens: 42 }, error: 'line one\rline two' };
  assert.equal(benchmarkScore(completed, 'total'), 0);
  assert.equal(benchmarkScore(completed, 'react'), null);
  assert.equal(benchmarkScore({ ...completed, status: 'running' }, 'bonus'), null);
  assert.equal(benchmarkScore({ ...completed, scores: { total: NaN } }, 'total'), null);
  const csv = benchmarkCSV([completed]);
  assert.ok(csv.startsWith('id,model,upstream_model,provider,model_revision,'));
  assert.ok(csv.includes("'=FORMULA()"));
  assert.ok(csv.includes('"model,""quoted"""'));
  assert.ok(csv.includes(',0,80,,,,50,'));
  assert.ok(csv.includes('"line one\rline two"'));
  assert.ok(csv.endsWith('\r\n'));
});
