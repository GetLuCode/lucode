import { test } from 'node:test'
import assert from 'node:assert/strict'
import { layoutGraph } from '../src/components/Git/gitGraph.ts'
import { languageOf, parseDiff, toSplitRows } from '../src/components/Git/gitDiff.ts'

const c = (sha: string, ...parents: string[]) => ({ sha, parents })

test('a straight history stays on one lane', () => {
  const { rows, lanes } = layoutGraph([c('c', 'b'), c('b', 'a'), c('a')])
  assert.equal(lanes, 1)
  assert.deepEqual(rows.map(r => r.lane), [0, 0, 0])
  assert.equal(rows[0].top.length, 0, 'nothing comes down into the newest commit')
  assert.equal(rows[2].bottom.length, 0, 'nothing goes down from the root')
})

test('a merge opens a lane for the branch and it closes where the branch left', () => {
  // m merges f into the line; f and b both come from a
  const { rows, lanes } = layoutGraph([c('m', 'b', 'f'), c('f', 'a'), c('b', 'a'), c('a')], 'm')
  assert.equal(lanes, 2)
  assert.equal(rows[0].merge, true)
  assert.deepEqual(rows.map(r => r.lane), [0, 1, 0, 0], 'the main line keeps lane 0')
  assert.deepEqual(rows[0].bottom.map(s => [s.from, s.to]), [[0, 0], [0, 1]])
  const last = rows[3]
  assert.deepEqual(last.top.map(s => s.from).sort(), [0, 1], 'both lines arrive at the fork')
  assert.ok(last.top.every(s => s.to === 0))
  assert.deepEqual(last.through, [])
})

test('the main line keeps lane 0 and its color even when a branch is newer', () => {
  const { rows } = layoutGraph([c('f2', 'f1'), c('f1', 'm1'), c('m2', 'm1'), c('m1')], 'm2')
  assert.equal(rows[0].lane, 1, 'the newer branch tip does not take the main lane')
  assert.equal(rows[2].lane, 0)
  assert.equal(rows[2].color, 0)
  assert.notEqual(rows[0].color, 0)
  assert.deepEqual(rows[1].bottom.map(s => [s.from, s.to]), [[1, 1]], 'the branch stays on its lane')
  assert.deepEqual(rows[3].top.map(s => [s.from, s.to]), [[0, 0], [1, 0]], 'and closes at the commit it left from')
  assert.equal(rows[2].bottom.filter(s => s.from === 0)[0].to, 0, 'the main line does not wander over to it')
})

test('two unrelated tips get their own lanes and their own colors', () => {
  const { rows } = layoutGraph([c('x'), c('y')])
  assert.equal(rows[0].lane, 0)
  assert.equal(rows[1].lane, 0, 'the lane of a finished line is free again')
  assert.notEqual(rows[0].color, rows[1].color)
})

test('lanes that pass a commit by are drawn straight through it', () => {
  const { rows } = layoutGraph([c('a2', 'a1'), c('b2', 'b1'), c('a1'), c('b1')])
  const b2 = rows[1]
  assert.equal(b2.lane, 1)
  assert.ok(b2.top.some(s => s.from === 0 && s.to === 0))
  assert.ok(b2.bottom.some(s => s.from === 0 && s.to === 0))
  assert.deepEqual(b2.through.map(t => t.lane), [0, 1])
})

test('a parent outside the loaded page leaves its line running off the bottom', () => {
  const { rows } = layoutGraph([c('b', 'a')], 'b')
  assert.deepEqual(rows[0].through.map(t => t.lane), [0])
  assert.deepEqual(rows[0].bottom.map(s => [s.from, s.to]), [[0, 0]])
})

test('an octopus merge opens one lane per parent', () => {
  const { rows, lanes } = layoutGraph([c('m', 'a', 'b', 'd'), c('a'), c('b'), c('d')])
  assert.equal(lanes, 3)
  assert.equal(rows[0].bottom.length, 3)
})

const SAMPLE = [
  'diff --git a/a.go b/a.go',
  'index 111..222 100644',
  '--- a/a.go',
  '+++ b/a.go',
  '@@ -10,3 +10,4 @@ func main() {',
  ' same',
  '-old',
  '+new one',
  '+new two',
  ' tail',
  '\\ No newline at end of file',
  '',
].join('\n')

test('a diff keeps the numbers of each side', () => {
  const { lines, binary, notes } = parseDiff(SAMPLE)
  assert.equal(binary, false)
  assert.deepEqual(notes, [])
  assert.equal(lines[0].kind, 'hunk')
  assert.equal(lines[0].text, 'func main() {')
  assert.deepEqual(lines.slice(1, 6).map(l => [l.kind, l.oldNo, l.newNo]), [
    ['ctx', 10, 10], ['del', 11, undefined], ['add', undefined, 11], ['add', undefined, 12], ['ctx', 12, 13],
  ])
  assert.equal(lines[6].kind, 'note')
})

test('the --- and +++ header lines are not read as a removal and an addition', () => {
  const { lines } = parseDiff(SAMPLE)
  assert.ok(!lines.some(l => l.text.includes('a/a.go')))
})

test('a removed line that looks like a header stays a removal inside a hunk', () => {
  const { lines } = parseDiff('@@ -1 +1 @@\n--- not a header\n+++ neither\n')
  assert.deepEqual(lines.slice(1).map(l => [l.kind, l.text]), [['del', '-- not a header'], ['add', '++ neither']])
})

test('windows line ends do not stay in the text', () => {
  const { lines } = parseDiff('@@ -1 +1 @@\r\n-a\r\n+b\r\n')
  assert.deepEqual(lines.slice(1).map(l => l.text), ['a', 'b'])
})

test('binary files and renames are said, not shown as lines', () => {
  assert.equal(parseDiff('diff --git a/x b/x\nBinary files a/x and b/x differ\n').binary, true)
  const r = parseDiff('diff --git a/x b/y\nsimilarity index 100%\nrename from x\nrename to y\n')
  assert.equal(r.notes.length, 3)
  assert.equal(r.lines.length, 0)
})

test('the split view pairs removals with the additions of the same change', () => {
  const rows = toSplitRows(parseDiff(SAMPLE).lines)
  assert.ok(rows[0].full)
  assert.equal(rows[1].left, rows[1].right, 'context is the same line on both sides')
  assert.equal(rows[2].left?.text, 'old')
  assert.equal(rows[2].right?.text, 'new one')
  assert.equal(rows[3].left, undefined)
  assert.equal(rows[3].right?.text, 'new two')
})

test('the language comes from the file name', () => {
  assert.equal(languageOf('internal/api/x.go'), 'go')
  assert.equal(languageOf('web/App.TSX'), 'typescript')
  assert.equal(languageOf('docker/Dockerfile'), 'dockerfile')
  assert.equal(languageOf('LICENSE'), '')
})
