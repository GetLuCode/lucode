// The port of internal/aimode against a fake lucode and a fake Google. No network, no real Google.
//   node --test internal/plugins/bundled/gemini/_test/port.test.mjs
// A folder starting with _ stays out of the embedded plugin and out of `lucode-plugin pack`.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdtempSync, rmSync, existsSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import vm from 'node:vm'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const TURN_COUNT = `document.querySelectorAll('[data-scope-id="turn"]').length`
const PRIMER = readFileSync(join(root, 'data/primer.md'), 'utf8').trim()
const MARKDOWN = readFileSync(join(root, 'data/markdown.js'), 'utf8')

const HOME = 'https://www.google.com/'
const AI = 'https://www.google.com/search?udm=50'

// A Google that answers what the test sets up. Time only moves when the plugin sleeps.
function makeWorld(over = {}) {
  const w = {
    clock: 1_000_000,
    settings: { headless: false, primer: '' },
    memory: new Map(),
    conversations: new Map(),
    progress: [],
    logs: [],
    calls: [],
    tabs: [{ id: 't1', url: 'about:blank', title: '', current: true, turns: 0 }],
    nextTab: 2,
    // what Google does
    consent: false,
    button: { role: 'link', name: 'AI Mode' },
    clickEnters: true,
    box: true,
    plainSearch: false,
    takesMessage: true,
    savedPages: {}, // url -> turns it ends up showing
    sorry: false,
    titleOnSorry: '',
    answers: {}, // per typed text; default "Answer to <text>"
    answerOf: null, // (polls) => text, to make an answer that keeps changing
    takesImage: true,
    attachResult: 'ok',
    chipRole: 'button',
    closeFails: false,
    ...over,
  }
  w.current = () => w.tabs.find(t => t.current)
  w.deadlineMs = over.deadlineMs ?? 720000
  return w
}

function fakeBrowser(w) {
  const log = (name, ...args) => w.calls.push([name, ...args])
  const go = url => {
    const t = w.current()
    t.url = url
    t.title = ''
    t.turns = 0
    if (w.sorry) { t.url = 'https://www.google.com/sorry/index?continue=x'; t.title = w.titleOnSorry }
    if (url === AI && w.plainSearch) t.url = 'https://www.google.com/search?q=x'
    if (Object.hasOwn(w.savedPages, url)) { t.turns = w.savedPages[url]; t.reopened = url }
  }
  return {
    launch: o => { log('launch', o); return 'launched' },
    close: () => {},
    status: () => 'running',
    tabs: () => w.tabs.map(({ id, url, title, current }) => ({ id, url, title, current })),
    newTab: url => {
      log('newTab', url)
      for (const t of w.tabs) t.current = false
      w.tabs.push({ id: 't' + w.nextTab++, url, title: '', current: true, turns: 0 })
    },
    switchTab: id => {
      log('switchTab', id)
      const t = w.tabs.find(x => x.id === id)
      if (!t) throw new Error('no such tab')
      for (const x of w.tabs) x.current = x === t
    },
    closeTab: id => {
      log('closeTab', id)
      if (w.closeFails) throw new Error('cannot')
      w.tabs = w.tabs.filter(t => t.id !== id)
      if (w.tabs.length && !w.current()) w.tabs[0].current = true
    },
    info: () => ({ url: w.current().url, title: w.current().title }),
    navigate: url => {
      log('navigate', url)
      if (!url.startsWith('https://www.google.com/')) throw new Error(`"${url}" is not on an origin this plugin may visit`)
      go(url)
    },
    find: q => {
      log('find', q)
      const t = w.current()
      if (q.role === 'button' && ['Aceptar todo', 'Accept all', 'Acepto', 'I agree'].includes(q.name)) {
        assert.equal(q.exact, true)
        return w.consent === q.name ? [{ ref: 'consent', name: q.name, role: 'button' }] : []
      }
      if (q.name && w.button && q.name === w.button.name && q.role === w.button.role && t.url === HOME) {
        assert.equal(q.exact, true)
        return [{ ref: 'aibutton', name: q.name, role: q.role }]
      }
      if (q.name && q.name.startsWith('image.')) {
        return w.takesImage && t.imageAttached && q.role === w.chipRole && q.exact && q.name === t.imageAttached ? [{ ref: 'chip', name: q.name }] : []
      }
      if (!q.name && q.role === 'textbox' && w.box && t.url !== HOME && t.url !== 'about:blank') return [{ ref: 'box', role: 'textbox', name: 'Buscar' }]
      return []
    },
    click: ref => {
      log('click', ref)
      if (ref === 'consent') { w.consent = false; return }
      if (ref === 'aibutton' && w.clickEnters) w.current().url = AI
      if (ref === 'box') w.current().focused = true
    },
    type: (ref, text, o) => {
      log('type', ref, text, o)
      assert.equal(o.submit, true)
      const t = w.current()
      if (w.takesMessage) {
        t.turns++
        t.lastAnswer = w.answers[text] ?? 'Answer to ' + text
        t.polls = 0
      }
    },
    press: () => {},
    evalPage: code => {
      const t = w.current()
      if (code === TURN_COUNT) return String(t.turns)
      if (code === MARKDOWN) {
        t.polls = (t.polls ?? 0) + 1
        w.answerPolls = (w.answerPolls ?? 0) + 1
        return w.answerOf ? w.answerOf(t.polls) : t.lastAnswer ?? ''
      }
      assert.ok(code.startsWith('(// Pastes a picture'), 'unexpected code')
      const m = /\)\("([^"]*)","([^"]*)"\)$/.exec(code)
      log('attach', m[1], m[2])
      if (w.attachResult === 'ok') t.imageAttached = 'image.' + m[2].replace('image/', '').replace('jpeg', 'jpg')
      return w.attachResult
    },
    text: () => '',
  }
}

function loadPlugin(w) {
  const lucode = {
    plugin: { id: 'lucode.gemini', name: 'Gemini', version: '1.0.0' },
    context: {
      projectId: 'p1', sessionId: 's1', target: 'server', deadlineMs: w.deadlineMs,
      cancelled: () => !!w.cancelAfter && w.clock >= w.cancelAfter,
      progress: text => w.progress.push(text),
    },
    log: { info() {}, warn: (...a) => w.logs.push(a), error() {} },
    data: { text: p => readFileSync(join(root, 'data', p), 'utf8') },
    settings: { get: k => w.settings[k] ?? null },
    memory: {
      get: k => (w.memory.has(k) ? JSON.parse(w.memory.get(k)) : null),
      set: (k, v) => w.memory.set(k, JSON.stringify(v)),
      delete: k => w.memory.delete(k),
    },
    time: { now: () => w.clock, sleep: ms => { w.clock += ms } },
    locks: {
      with: (name, fn) => {
        w.calls.push(['lock', name])
        if (w.lockRefuses && name === 'turn') throw new Error(`cancelled while waiting for the lock "${name}"`) // the host's words
        return fn()
      },
    },
    conversations: {
      get: id => w.conversations.get(id) ?? null,
      save: c => {
        const had = w.conversations.get(c.sessionId)
        w.conversations.set(c.sessionId, { ...c, title: had?.title || c.title })
      },
    },
    browser: fakeBrowser(w),
  }
  const cache = {}
  const req = from => spec => {
    const file = join(dirname(from), spec)
    if (cache[file]) return cache[file].exports
    const module = { exports: {} }
    cache[file] = module
    const wrapped = vm.runInNewContext(`(function(module,exports,require,lucode){${readFileSync(file, 'utf8')}\n})`)
    wrapped(module, module.exports, req(file), lucode)
    return module.exports
  }
  return req(join(root, 'x'))('./main.js')
}

const turn = (w, message, extra = {}) => loadPlugin(w).models.aimode.turn({ sessionId: 's1', projectId: 'p1', message, image: null, model: 'aimode', ...extra })
const typed = w => w.calls.filter(c => c[0] === 'type').map(c => c[2])
const called = (w, name) => w.calls.filter(c => c[0] === name)

// The thrown value is a plain string, so the host shows exactly these words.
function failsWith(fn, text) {
  try {
    fn()
  } catch (e) {
    assert.equal(typeof e, 'string', 'must throw a string: ' + e)
    assert.equal(e, text)
    return
  }
  assert.fail('expected: ' + text)
}

const lib = () => {
  const module = { exports: {} }
  vm.runInNewContext(`(function(module){${readFileSync(join(root, 'lib/text.js'), 'utf8')}\n})`)(module)
  return module.exports
}

// ---- aimode_test.go ---------------------------------------------------------------------------

test('Clean removes the page noise', () => {
  const { clean } = lib()
  const raw = 'Un motor transforma energía.\nIFEMA Madrid\n +3\nAdmisión: baja el pistón.\n\n\n\nReferencias web:\nRACE: https://example.com/race\nLa IA puede cometer errores, así que verifica las respuestas\n'
  assert.equal(clean(raw), 'Un motor transforma energía.\nIFEMA Madrid\nAdmisión: baja el pistón.\n\nReferencias web:\nRACE: https://example.com/race')
  assert.equal(clean('Hola\nAI can make mistakes, so double-check responses'), 'Hola')
  assert.equal(clean('   \n'), '')
  assert.equal(clean('a  \t\r\nb'), 'a\nb')
})

test('title is the question on one line, cut at 100 characters', () => {
  const { title } = lib()
  assert.equal(title('  uno\n dos\t tres '), 'uno dos tres')
  assert.equal(title('ñ'.repeat(100)), 'ñ'.repeat(100))
  assert.equal(title('ñ'.repeat(101)), 'ñ'.repeat(100) + '…')
  assert.equal(title('😀'.repeat(101)), '😀'.repeat(100) + '…')
})

test('a conversation: instructions once, then each message in the same tab', () => {
  const w = makeWorld()
  assert.equal(turn(w, '¿Capital de Francia?'), 'Answer to ¿Capital de Francia?')
  assert.deepEqual(typed(w), [PRIMER, '¿Capital de Francia?'])
  assert.deepEqual(w.progress, [
    'Opening Edge…', 'Starting a new conversation in AI Mode…', 'Giving Gemini its instructions…',
    "Waiting for Google's answer…", "Waiting for Google's answer…",
  ])
  assert.equal(turn(w, '¿Y la de Italia?'), 'Answer to ¿Y la de Italia?')
  assert.deepEqual(typed(w), [PRIMER, '¿Capital de Francia?', '¿Y la de Italia?'])
  assert.equal(w.tabs.length, 1, 'the blank tab Edge opened with was used')
  assert.equal(w.current().turns, 3)
  const saved = w.conversations.get('s1')
  assert.equal(saved.url, AI)
  assert.equal(saved.turns, 3)
  assert.equal(saved.title, '¿Capital de Francia?', 'the first question stays the title')
  assert.equal(saved.source, 'aimode')
})

test('a message with several lines goes whole', () => {
  const w = makeWorld()
  turn(w, 'Responde solo con el resultado:\nuno más\ndos')
  assert.equal(typed(w)[1], 'Responde solo con el resultado:\nuno más\ndos')
})

test('after a restart the saved url reopens the conversation, with no instructions', () => {
  const w = makeWorld()
  const url = 'https://www.google.com/search?udm=50&mtid=abc'
  w.savedPages[url] = 2
  w.conversations.set('s1', { sessionId: 's1', url, turns: 2, title: 'Tokio' })
  const a = turn(w, '¿En qué país?')
  assert.equal(a, 'Answer to ¿En qué país?')
  assert.deepEqual(typed(w), ['¿En qué país?'])
  assert.equal(w.current().turns, 3)
  assert.equal(w.conversations.get('s1').title, 'Tokio')
  assert.ok(w.progress.includes('Reopening the saved conversation…'))
  assert.ok(w.progress.includes('Reopening the saved conversation… 2 of 2 turns loaded'))
  assert.equal(called(w, 'navigate')[0][1], url)
})

test('a saved conversation that does not load is given up, and the answer says so', () => {
  const w = makeWorld()
  const url = 'https://www.google.com/search?udm=50&mtid=gone'
  w.savedPages[url] = 1 // fewer than the 40 it had
  w.conversations.set('s1', { sessionId: 's1', url, turns: 40, title: 'x' })
  const started = w.clock
  const a = turn(w, 'hola')
  assert.equal(a, '_The saved Google conversation could not be reopened (Google no longer has it for this browser profile), so this is a new one._\n\nAnswer to hola')
  assert.deepEqual(typed(w), [PRIMER, 'hola'])
  assert.ok(w.progress.includes('Reopening the saved conversation… 1 of 40 turns loaded'))
  assert.ok(w.clock - started >= 20 * 500, 'it waited its 20 polls')
  assert.ok(w.clock - started < 3 * 60 * 1000, 'and not the 3 minutes')
  assert.equal(w.current().turns, 2)
})

test('a saved url the permission refuses counts as lost, not as an error', () => {
  const w = makeWorld()
  w.conversations.set('s1', { sessionId: 's1', url: 'https://www.google.es/x', turns: 2 })
  const a = turn(w, 'hola')
  assert.match(a, /^_The saved Google conversation could not be reopened/)
})

test('the user closed the tab: it starts over', () => {
  const w = makeWorld()
  turn(w, 'uno')
  w.conversations.clear()
  w.tabs = [{ id: 't9', url: 'https://www.bing.com/', title: '', current: true, turns: 0 }]
  turn(w, 'dos')
  assert.deepEqual(typed(w), [PRIMER, 'uno', PRIMER, 'dos'])
  assert.deepEqual(called(w, 'newTab').at(-1), ['newTab', 'about:blank'])
})

test('a second session gets a tab of its own', () => {
  const w = makeWorld()
  turn(w, 'uno')
  turn(w, 'dos', { sessionId: 's2' })
  assert.equal(w.tabs.length, 2)
  assert.deepEqual(called(w, 'newTab'), [['newTab', 'about:blank']])
  assert.deepEqual(JSON.parse(w.memory.get('tabs')), { s1: 't1', s2: 't2' })
})

// ---- entering AI Mode -------------------------------------------------------------------------

for (const name of ['Aceptar todo', 'Accept all', 'Acepto', 'I agree']) {
  test(`the consent dialog "${name}" is accepted`, () => {
    const w = makeWorld({ consent: name })
    turn(w, 'hola')
    assert.deepEqual(called(w, 'click')[0], ['click', 'consent'])
  })
}

for (const [role, name] of [['link', 'Modo IA'], ['button', 'Modo IA'], ['link', 'AI Mode'], ['button', 'AI Mode'], ['link', 'Modo de IA'], ['button', 'Modo de IA']]) {
  test(`enters by the ${role} "${name}"`, () => {
    const w = makeWorld({ button: { role, name } })
    turn(w, 'hola')
    assert.ok(called(w, 'click').some(c => c[1] === 'aibutton'))
    assert.deepEqual(called(w, 'navigate').map(c => c[1]), [HOME], 'no need for the address')
  })
}

test('a click that does nothing falls back to the address', () => {
  const w = makeWorld({ clickEnters: false })
  turn(w, 'hola')
  assert.deepEqual(called(w, 'navigate').map(c => c[1]), [HOME, AI])
})

test('no button falls back to the address', () => {
  const w = makeWorld({ button: null })
  turn(w, 'hola')
  assert.deepEqual(called(w, 'navigate').map(c => c[1]), [HOME, AI])
})

test('no message box', () => {
  const w = makeWorld({ box: false })
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: AI Mode did not show a message box (it may not be available for this account or region)')
  assert.deepEqual(called(w, 'closeTab').map(c => c[1]), ['t1'], 'its tab closes')
  assert.equal(JSON.parse(w.memory.get('tabs') ?? '{}').s1, undefined)
})

test('a plain search box is never typed into', () => {
  const w = makeWorld({ button: null, plainSearch: true })
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: Google showed its plain search instead of AI Mode (it may be asking to verify you are human, or AI Mode is not available for this account or region)')
  assert.deepEqual(typed(w), [])
})

const HUMAN = 'Google AI Mode: Google is asking to verify that you are human. Solve the check in the Edge window lucode opened, then send the message again (the browser profile keeps the result)'
test('a human check, with the window showing', () => {
  const w = makeWorld({ sorry: true, box: false })
  failsWith(() => turn(w, 'hola'), HUMAN)
})

test('a human check by the page title', () => {
  const w = makeWorld({ box: false })
  const p = loadPlugin(w)
  w.titleOnSorry = 'Unusual traffic'
  w.sorry = true
  failsWith(() => p.models.aimode.turn({ sessionId: 's1', message: 'x' }), HUMAN)
})

test('a human check with the window hidden says how to show it', () => {
  const w = makeWorld({ sorry: true, box: false })
  w.settings.headless = true
  failsWith(() => turn(w, 'hola'), "Google AI Mode: Google is asking to verify that you are human, and Edge is hidden (the plugin's Hide the Edge window setting). Turn that setting off so the window shows, solve the check there, and send the message again")
  assert.equal(called(w, 'launch')[0][1].headless, true)
})

// ---- sending ----------------------------------------------------------------------------------

test('a message Google does not take', () => {
  const w = makeWorld({ takesMessage: false })
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: priming the conversation: Google did not take the message')
})

test('an answer counts once it stops changing for 5 polls', () => {
  const w = makeWorld({ answerOf: polls => (polls < 4 ? 'parte ' + polls : 'final') })
  w.answerPolls = 0
  assert.equal(turn(w, 'hola'), 'final')
})

test('an answer that never settles is returned late rather than not at all', () => {
  const w = makeWorld()
  const p = loadPlugin(w)
  // once it is the real question, the text keeps changing
  w.answerOf = polls => (w.current().turns >= 2 ? 'cambia ' + polls : 'Listo.')
  const started = w.clock
  assert.match(p.models.aimode.turn({ sessionId: 's1', message: 'hola' }), /^cambia \d+$/)
  assert.ok(w.clock - started >= 3 * 60 * 1000)
})

test('no answer at all', () => {
  const w = makeWorld({ answerOf: () => '   ' })
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: priming the conversation: AI Mode did not answer in time')
})

test('an empty message', () => {
  const w = makeWorld()
  failsWith(() => turn(w, '  \n'), 'Google AI Mode: empty message')
  assert.deepEqual(called(w, 'launch'), [])
})

test('the whole turn has a ceiling', () => {
  const w = makeWorld({ deadlineMs: 30000, answerOf: () => 'cambia ' + Math.random() })
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: priming the conversation: context deadline exceeded')
})

test('an image goes in with its chip waited for', () => {
  const w = makeWorld()
  const a = turn(w, 'mira', { image: { base64: 'QUJD', mediaType: 'image/jpeg' } })
  assert.equal(a, 'Answer to mira')
  assert.deepEqual(called(w, 'attach'), [['attach', 'QUJD', 'image/jpeg']])
  assert.ok(w.progress.includes('Attaching the image…'))
  assert.ok(called(w, 'find').some(c => c[1].name === 'image.jpg' && c[1].role === 'button' && c[1].exact))
  const order = w.calls.map(c => c[0])
  assert.ok(order.lastIndexOf('click') < order.lastIndexOf('attach'), 'the box takes the focus first')
  assert.ok(order.lastIndexOf('attach') < order.lastIndexOf('type'))
})

test('an image the page refuses', () => {
  const w = makeWorld({ attachResult: 'the message box is not on the page' })
  failsWith(() => turn(w, 'mira', { image: { base64: 'QUJD', mediaType: 'image/png' } }), 'Google AI Mode: attaching the image: the message box is not on the page')
})

test('an image Google never takes', () => {
  const w = makeWorld({ takesImage: false })
  failsWith(() => turn(w, 'mira', { image: { base64: 'QUJD', mediaType: 'image/png' } }), 'Google AI Mode: Google did not take the image')
})

// ---- one question at a time, settings, the rest ------------------------------------------------

test('one question at a time, saying so while it waits', () => {
  const w = makeWorld()
  w.memory.set('busy_until', JSON.stringify(w.clock + 60000))
  turn(w, 'hola')
  assert.equal(w.progress[0], 'Waiting for another Gemini conversation to finish…')
  assert.ok(w.calls.some(c => c[0] === 'lock' && c[1] === 'turn'))
  assert.equal(w.memory.has('busy_until'), false, 'it is free again')
})

test('nobody else running: no waiting text, even with an old mark left behind', () => {
  const w = makeWorld()
  w.memory.set('busy_until', JSON.stringify(w.clock - 1))
  turn(w, 'hola')
  assert.equal(w.progress[0], 'Opening Edge…')
})

test('stopped while waiting for its place', () => {
  const w = makeWorld({ lockRefuses: true })
  w.cancelAfter = w.clock
  failsWith(() => turn(w, 'hola'), 'Google AI Mode: gave up waiting for another Gemini conversation to finish: context canceled')
})

test('the primer: the setting replaces the one in data', () => {
  const w = makeWorld()
  w.settings.primer = '  Sé breve.  '
  turn(w, 'hola')
  assert.deepEqual(typed(w), ['Sé breve.', 'hola'])
  assert.ok(PRIMER.startsWith('A partir de ahora eres el asistente de un chat.'))
  assert.ok(PRIMER.endsWith('Si entendiste, responde solo: Listo.'))
})

test('headless comes from the setting', () => {
  const w = makeWorld()
  turn(w, 'hola')
  assert.equal(called(w, 'launch')[0][1].headless, false)
})

test('a closed session closes its tab', () => {
  const w = makeWorld()
  turn(w, 'hola')
  const p = loadPlugin(w)
  p.events.sessionClosed({ sessionId: 's1', projectId: 'p1' })
  assert.deepEqual(called(w, 'closeTab').map(c => c[1]), ['t1'])
  assert.deepEqual(JSON.parse(w.memory.get('tabs')), {})
  p.events.sessionClosed({ sessionId: 'never-had-one' })
  assert.equal(called(w, 'closeTab').length, 1)
})

test('a session closed during a turn closes its tab when the turn ends', () => {
  const w = makeWorld()
  turn(w, 'uno')
  turn(w, 'dos', { sessionId: 's2' })
  w.memory.set('busy_until', JSON.stringify(w.clock + 60000))
  loadPlugin(w).events.sessionClosed({ sessionId: 's1' })
  assert.equal(called(w, 'closeTab').length, 0, 'not during the turn')
  turn(w, 'tres', { sessionId: 's2' })
  assert.deepEqual(called(w, 'closeTab').map(c => c[1]), ['t1'])
})

test('a flow node asks in a conversation of its own and closes it', () => {
  const w = makeWorld()
  const p = loadPlugin(w)
  const out = p.nodes.ask({ config: { question: '¿Qué es Go?' }, run: { id: 'r1' }, projectId: 'p1' })
  assert.deepEqual(JSON.parse(JSON.stringify(out)), { port: 'next', output: { answer: 'Answer to ¿Qué es Go?' } })
  assert.equal(w.conversations.size, 0, 'nothing in the history')
  assert.equal(called(w, 'closeTab').length, 1)
})

test('a flow node failure has the same words', () => {
  const w = makeWorld({ box: false })
  failsWith(() => loadPlugin(w).nodes.ask({ config: { question: 'x' } }), 'Google AI Mode: AI Mode did not show a message box (it may not be available for this account or region)')
})

test('the data files are the ones the Go package has, unchanged', () => {
  const go = join(root, '../../../aimode')
  if (!existsSync(go)) return // it goes away once this plugin takes over
  assert.equal(MARKDOWN, readFileSync(join(go, 'markdown.js'), 'utf8'))
  assert.equal(readFileSync(join(root, 'data/attach.js'), 'utf8'), readFileSync(join(go, 'attach.js'), 'utf8'))
})

// ---- markdown_test.go: the real markdown.js in a real Edge, offline ---------------------------

const fakeAnswer = `<!doctype html><body>
<div data-scope-id="turn"><div data-container-id="main-col">
 <div role="heading" aria-level="3">Elementos <!--x--></div>
 <h4>Una sección</h4>
 <div role="heading" aria-level="5">Un detalle</div>
 <h6>Una nota</h6>
 <div style="display:block">Un párrafo con <strong>negritas</strong> y <em>cursiva</em>, y un enlace a
   <div style="display:inline"><a href="/goto?url=CAESabc">Wikipedia<!--TgQPHd|||[[&quot;https://es.wikipedia.org/wiki/A?x=1&amp;y=2&quot;,null,[1]]]--></a></div>.
   <button>Copiar</button></div>
 <ul><li><span>Uno</span></li><li><span>Dos</span><button>x</button></li><li><button>solo un botón</button></li></ul>
 <ol><li>Primero</li><li>Segundo</li></ol>
 <div><div><div>python</div></div><div><pre><code>def f(x):
    return x</code></pre></div></div>
 <div>Usa el código con precaución.</div>
 <table><tbody><tr><th>A</th><th>B | C</th></tr><tr><td>1</td><td>2</td></tr></tbody></table>
 <div style="display:none">Gracias por hacérnoslo saber</div>
 <div>Referencias:</div>
 <ul><li><span>Sitio oficial: </span><div style="display:inline"><span><a href="/goto?url=CAESdef">python.org<!--TgQPHd|||[[&quot;http://python.org&quot;,null,[1]]]--></a></span></div></li></ul>
 <div>La IA puede cometer errores, así que verifica las respuestas</div>
</div></div></body>`

const EDGE = ['C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', 'C:/Program Files/Microsoft/Edge/Application/msedge.exe', '/usr/bin/microsoft-edge'].find(existsSync)

test('markdown conversion', { skip: EDGE ? false : 'Edge is not installed' }, () => {
  const dir = mkdtempSync(join(tmpdir(), 'gemini-md-'))
  try {
    // the page runs markdown.js on itself and leaves the result where --dump-dom shows it
    const html = fakeAnswer.replace('</body>', `<script>
      const out = ${MARKDOWN};
      const b = document.createElement('textarea');
      b.id = 'result';
      b.textContent = '';
      b.setAttribute('data-out', encodeURIComponent(out));
      document.documentElement.appendChild(b);
    </script></body>`)
    const page = join(dir, 'page.html')
    writeFileSync(page, '\ufeff' + html, 'utf8')
    const dom = execFileSync(EDGE, ['--headless=new', '--disable-gpu', '--no-first-run', `--user-data-dir=${join(dir, 'profile')}`, '--virtual-time-budget=2000', '--dump-dom', pathToFileURL(page).href], { encoding: 'utf8', timeout: 60000, stdio: ['ignore', 'pipe', 'ignore'] })
    const m = /data-out="([^"]*)"/.exec(dom)
    assert.ok(m, 'the page did not run')
    const got = decodeURIComponent(m[1])
    // Google's h4 and h5 are shown as h2 and h3; the other levels stay
    const want = '### Elementos\n\n' +
      '## Una sección\n\n' +
      '### Un detalle\n\n' +
      '###### Una nota\n\n' +
      'Un párrafo con **negritas** y *cursiva*, y un enlace a [Wikipedia](https://es.wikipedia.org/wiki/A?x=1&y=2).\n\n' +
      '- Uno\n- Dos\n\n' +
      '1. Primero\n2. Segundo\n\n' +
      '```python\ndef f(x):\n    return x\n```\n\n' +
      '| A | B \\| C |\n| --- | --- |\n| 1 | 2 |\n\n' +
      'Referencias:\n\n' +
      '- Sitio oficial: [python.org](http://python.org)'
    assert.equal(lib().clean(got), want)
  } finally {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* Edge may still hold it for a moment */ }
  }
})
