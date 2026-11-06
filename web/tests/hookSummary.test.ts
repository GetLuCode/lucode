// Run with: node --test tests/hookSummary.test.ts   (Node 24 strips the types itself)
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  HOOK_TIMEOUT_SECONDS,
  formatCountdown,
  hookAction,
  hookOutcome,
  hookSecondsLeft,
  hookSummary,
  splitPath,
} from '../src/components/Hook/hookSummary.ts'

test('hookAction names known tools and has a neutral fallback for the rest', () => {
  assert.equal(hookAction('Bash'), 'Run a command')
  assert.equal(hookAction('mcp__hub__task_list'), 'Use a tool')
})

test('hookOutcome covers every decision the server stores', () => {
  assert.deepEqual(hookOutcome('approve'), { label: 'Approved', outcome: 'allowed' })
  assert.deepEqual(hookOutcome('allow_always'), { label: 'Allowed always', outcome: 'allowed' })
  assert.deepEqual(hookOutcome('allow_with_rule'), { label: 'Allowed by rule', outcome: 'allowed' })
  assert.deepEqual(hookOutcome('deny'), { label: 'Denied', outcome: 'denied' })
  assert.deepEqual(hookOutcome('deny_always'), { label: 'Denied always', outcome: 'denied' })
  assert.deepEqual(hookOutcome('timeout'), { label: 'Timed out', outcome: 'timeout' })
})

test('hookOutcome shows a decision it does not know instead of hiding it', () => {
  assert.deepEqual(hookOutcome('later'), { label: 'later', outcome: 'unknown' })
  assert.deepEqual(hookOutcome(''), { label: 'Answered', outcome: 'unknown' })
})

test('hookSummary takes the first non-empty line of a command', () => {
  assert.equal(hookSummary('Bash', { command: '\n  git status\r\ngit push' }), 'git status')
})

test('hookSummary uses what identifies each tool', () => {
  assert.equal(hookSummary('Edit', { file_path: 'E:\\dev\\a.ts', old_string: 'x' }), 'E:\\dev\\a.ts')
  assert.equal(hookSummary('Grep', { pattern: 'foo', path: 'src' }), 'foo')
  assert.equal(hookSummary('Skill', { skill: 'run' }), 'run')
  assert.equal(hookSummary('mcp__x', { count: 3, query: 'hello\nworld' }), 'hello')
})

test('hookSummary survives input that is not an object', () => {
  assert.equal(hookSummary('Bash', null), '')
  assert.equal(hookSummary('Other', 'text'), '')
  assert.equal(hookSummary('Bash', { command: 42 }), '')
})

test('splitPath handles Unix and Windows paths', () => {
  assert.deepEqual(splitPath('/home/me/a.ts'), { dir: '/home/me/', file: 'a.ts' })
  assert.deepEqual(splitPath('E:\\dev\\lucode\\a.ts'), { dir: 'E:\\dev\\lucode\\', file: 'a.ts' })
  assert.deepEqual(splitPath('E:\\dev/mixed/a.ts'), { dir: 'E:\\dev/mixed/', file: 'a.ts' })
  assert.deepEqual(splitPath('a.ts'), { dir: '', file: 'a.ts' })
})

test('hookSecondsLeft counts down from the creation date and stops at zero', () => {
  const created = '2026-11-02T22:00:00.000Z'
  const at = (s: number) => new Date(created).getTime() + s * 1000
  assert.equal(hookSecondsLeft(created, at(0)), HOOK_TIMEOUT_SECONDS)
  assert.equal(hookSecondsLeft(created, at(100)), HOOK_TIMEOUT_SECONDS - 100)
  assert.equal(hookSecondsLeft(created, at(HOOK_TIMEOUT_SECONDS + 5)), 0)
})

test('hookSecondsLeft does not exceed the limit when the clocks disagree', () => {
  const created = '2026-11-02T22:00:00.000Z'
  assert.equal(hookSecondsLeft(created, new Date(created).getTime() - 60_000), HOOK_TIMEOUT_SECONDS)
})

test('hookSecondsLeft treats a missing or broken date as just created', () => {
  assert.equal(hookSecondsLeft(undefined, Date.now()), HOOK_TIMEOUT_SECONDS)
  assert.equal(hookSecondsLeft('nonsense', Date.now()), HOOK_TIMEOUT_SECONDS)
})

test('formatCountdown pads the seconds', () => {
  assert.equal(formatCountdown(240), '4:00')
  assert.equal(formatCountdown(65), '1:05')
  assert.equal(formatCountdown(0), '0:00')
})
