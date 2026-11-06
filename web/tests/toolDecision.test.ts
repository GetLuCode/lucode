import test from 'node:test'
import assert from 'node:assert/strict'
import { toolDecisionLabel } from '../src/components/Chat/toolDecision.ts'

test('a logged tool call says who decided it', () => {
  assert.equal(toolDecisionLabel('allow', 'rule'), 'Allowed by a rule')
  assert.equal(toolDecisionLabel('deny', 'flow'), 'Denied by a flow')
  assert.equal(toolDecisionLabel('allow', 'lucode'), 'Allowed')
  assert.equal(toolDecisionLabel('auto', 'auto'), 'Claude auto mode')
})
