import test from 'node:test'
import assert from 'node:assert/strict'
import { highlightCode } from '../src/lib/highlight.ts'

test('highlights a known language', () => {
  const { html, language } = highlightCode('def f(x):\n    return "a"', 'python')
  assert.equal(language, 'python')
  assert.match(html, /<span class="hljs-keyword">def<\/span>/)
  assert.match(html, /<span class="hljs-string">/)
})

test('understands the usual aliases', () => {
  for (const lang of ['js', 'ts', 'sh', 'shell', 'yml', 'html', 'ps1', 'golang']) {
    assert.notEqual(highlightCode('x', lang).language, '', lang)
  }
})

test('an unknown or missing language comes back plain and escaped', () => {
  for (const lang of [undefined, '', 'klingon']) {
    const { html, language } = highlightCode('<script>alert("x")</script> & more', lang)
    assert.equal(language, '')
    assert.equal(html, '&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt; &amp; more')
  }
})

test('markup inside code is escaped even when highlighted', () => {
  const { html } = highlightCode('<script>alert(1)</script>', 'html')
  assert.ok(!html.includes('<script>'))
})

test('takes only the first word of the info string', () => {
  assert.equal(highlightCode('x = 1', 'python title="a.py"').language, 'python')
})
