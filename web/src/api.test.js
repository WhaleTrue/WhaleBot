import test from 'node:test';
import assert from 'node:assert/strict';
import { consumeSSE, nodeID } from './api.js';

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
