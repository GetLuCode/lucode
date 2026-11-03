// Run with: node --test tests/pendingHooks.test.ts   (Node 24 strips the types itself)
import test from 'node:test'
import assert from 'node:assert/strict'

import { pendingHookMessage, withPendingHooks } from '../src/store/pendingHooks.ts'
import type { Message, PendingHook } from '../src/types.ts'

const hook = (id: string, extra: Partial<PendingHook> = {}): PendingHook => ({
  session_id: 's1',
  tool_use_id: id,
  tool_name: 'Bash',
  tool_input: { command: 'ls' },
  created_at: '2026-11-02T22:00:00.000Z',
  ...extra,
})

const stored = (id: string, role: Message['role'], content: string, seq: number): Message => ({
  id, session_id: 's1', role, content, seq, created_at: '2026-11-02T21:00:00.000Z',
})

test('a pending hook becomes a chat message the approval UI can render', () => {
  const m = pendingHookMessage(hook('toolu_1'), 3)
  assert.equal(m.id, 'hook-toolu_1')
  assert.equal(m.role, 'hook')
  assert.equal(m.seq, 3)
  const body = JSON.parse(m.content)
  assert.equal(body.tool_use_id, 'toolu_1')
  assert.equal(body.tool_name, 'Bash')
  assert.deepEqual(body.tool_input, { command: 'ls' })
  assert.equal(body.decision, 'pending')
})

// The bug: opening a conversation from the approval toast reloads its messages from
// the API, which has no pending approvals, so the request vanished from the chat.
test('messages reloaded from the API get the pending approval back', () => {
  const fromApi = [
    stored('m1', 'user', '# Task: Fix login', 0),
    stored('m2', 'assistant', 'I will look at the code.', 1),
  ]
  const result = withPendingHooks(fromApi, [hook('toolu_1')])

  assert.equal(result.length, 3)
  assert.equal(result[2].id, 'hook-toolu_1')
  assert.equal(result[2].role, 'hook')
  assert.equal(result[2].seq, 2, 'it continues the sequence')
  assert.equal(JSON.parse(result[2].content).decision, 'pending')
  assert.equal(fromApi.length, 2, 'the input is not mutated')
})

test('a brand-new session with a pending approval shows it', () => {
  const result = withPendingHooks([], [hook('toolu_1')])
  assert.equal(result.length, 1)
  assert.equal(result[0].role, 'hook')
})

test('nothing to restore returns the very same array', () => {
  const msgs = [stored('m1', 'user', 'hi', 0)]
  assert.equal(withPendingHooks(msgs, []), msgs)
})

test('a hook that is already in the chat is not added twice', () => {
  const once = withPendingHooks([stored('m1', 'user', 'hi', 0)], [hook('toolu_1')])
  assert.equal(withPendingHooks(once, [hook('toolu_1')]), once, 'idempotent, same reference')
})

test('an answer the server just stored counts as present', () => {
  const answered = stored('uuid-1', 'hook', JSON.stringify({ tool_use_id: 'toolu_1', decision: 'approve' }), 5)
  const msgs = [stored('m1', 'user', 'hi', 0), answered]
  assert.equal(withPendingHooks(msgs, [hook('toolu_1')]), msgs, 'no pending duplicate next to the stored answer')
})

test('several pending approvals are restored in order', () => {
  const result = withPendingHooks([stored('m1', 'user', 'hi', 0)], [hook('toolu_a'), hook('toolu_b')])
  assert.deepEqual(result.map(m => m.id), ['m1', 'hook-toolu_a', 'hook-toolu_b'])
  assert.deepEqual(result.map(m => m.seq), [0, 1, 2])
})

test('only the missing approvals are added', () => {
  const have = withPendingHooks([stored('m1', 'user', 'hi', 0)], [hook('toolu_a')])
  const result = withPendingHooks(have, [hook('toolu_a'), hook('toolu_b')])
  assert.deepEqual(result.map(m => m.id), ['m1', 'hook-toolu_a', 'hook-toolu_b'])
})
