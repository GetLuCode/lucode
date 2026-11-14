import { test } from 'node:test'
import assert from 'node:assert/strict'
import { envProblem } from '../src/components/Commands/commandsTypes.ts'

test('envProblem accepts valid names and ignores empty rows', () => {
  assert.equal(envProblem([]), '')
  assert.equal(envProblem([{ name: 'NODE_ENV', value: 'dev' }, { name: '', value: '' }, { name: '_x1', value: '' }]), '')
})

test('envProblem says what is wrong', () => {
  assert.match(envProblem([{ name: '', value: 'x' }]), /no name/)
  assert.match(envProblem([{ name: '1A', value: 'x' }]), /not a valid name/)
  assert.match(envProblem([{ name: 'A-B', value: 'x' }]), /not a valid name/)
  assert.match(envProblem([{ name: 'A=B', value: 'x' }]), /not a valid name/)
  assert.match(envProblem([{ name: 'Path', value: '1' }, { name: 'PATH', value: '2' }]), /twice/)
})
