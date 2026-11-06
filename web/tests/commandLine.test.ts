import test from 'node:test'
import assert from 'node:assert/strict'
import { commandLine, commandSummary, splitArg } from '../src/lib/commandLine.ts'

test('plain arguments stay as they are', () => {
  assert.equal(commandLine({ exe: 'claude', args: ['-p', '--model=haiku'] }), 'claude -p --model=haiku')
})

test('arguments with spaces, quotes or backslashes are quoted and escaped', () => {
  const line = commandLine({ exe: 'C:\\bin\\claude.exe', args: ['--append-system-prompt=say "hi"\nnow', '--cwd=C:\\a b'] })
  // a Windows path without spaces is left as typed; the others are quoted with " and \ escaped
  assert.equal(line, 'C:\\bin\\claude.exe "--append-system-prompt=say \\"hi\\"\\nnow" "--cwd=C:\\\\a b"')
})

test('splits a flag from its value', () => {
  assert.deepEqual(splitArg('--model=haiku'), { flag: '--model', value: 'haiku' })
  assert.deepEqual(splitArg('--a=b=c'), { flag: '--a', value: 'b=c' })
  assert.deepEqual(splitArg('-p'), { flag: '-p', value: '' })
})

test('the summary keeps the flags that say what the run was', () => {
  const s = commandSummary({ args: ['-p', '--model=haiku', '--session-id=11111111-2222-3333-4444-555555555555', '--tools=Bash,Edit', '--worktree=lucode-1234'] })
  assert.equal(s, '--model=haiku --session-id=11111111… --worktree=lucode-1234')
})
