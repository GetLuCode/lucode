package stt

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// Endpointer decides when a phrase is over, instead of waiting for the worker.
//
// The worker closes a phrase with an audio silence detector, which with the CPU model
// can take seconds and pads the recording with silence (that ruined the final pass).
// The partial text is a better clock: when it has not changed for Idle, the speaker
// stopped. The phrase is then closed here: the fullest partial is sent as the final
// right away and the worker is told to flush; the final it produces afterwards for that
// phrase is dropped. A phrase with no text, or only a filler such as "Okay.", is noise:
// it is closed the same way but nothing is sent.
//
// The worker also has a defect this guards against: right after a phrase is closed, the
// first partials of the NEXT recording repeat the previous phrase's text (its realtime
// stage still holds the old buffer). Such a partial, seen before any different text, is
// stale: it is not shown and does not count as speech, or the old phrase would be written
// again.
//
// Events come from the worker's reader goroutine and Tick from a timer, so every
// method locks.
type Endpointer struct {
	Idle time.Duration

	mu        sync.Mutex
	phrase    Phrase
	active    bool      // a phrase is in progress
	lastShown string    // last partial forwarded to the page
	changedAt time.Time // when the page last got different text (or the phrase started)
	closed    bool      // closed here: the worker's final for it is still to come and is dropped
	lastFinal string    // normalized text of the last phrase sent
	fresh     bool      // a partial different from lastFinal has been seen in this phrase
	suspect   bool      // only stale-looking partials so far

	// A phrase closed here whose worker final had not arrived when the next phrase began: the
	// worker can take seconds to answer a flush, so that late final lands after SPEECH_START of
	// the next phrase. It must still be dropped (it is the old phrase, usually longer by the
	// words the partials had not caught up with), not delivered as if it were the new one.
	staleText    string // normalized text that was sent for it
	stalePending bool
	staleSpoken  int // phrases that began while it was pending
}

// The worker keeps its stale text across listening sessions too (the first partials of a new
// session echo the last phrase of the previous one), so the last phrase sent is remembered
// for the life of the process, not just of the session.
var (
	lastSentMu sync.Mutex
	lastSent   string
)

func rememberSent(n string) {
	if n == "" {
		return
	}
	lastSentMu.Lock()
	lastSent = n
	lastSentMu.Unlock()
}

// NewEndpointer closes phrases after idle without changes (idle <= 0 disables it).
func NewEndpointer(idle time.Duration) *Endpointer {
	lastSentMu.Lock()
	defer lastSentMu.Unlock()
	return &Endpointer{Idle: idle, lastFinal: lastSent}
}

// Speech marks the start of a phrase (SPEECH_START).
func (e *Endpointer) Speech(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed && e.staleText != "" { // closed here, the worker's final still to come: keep expecting it
		e.stalePending = true
		e.staleSpoken = 0
	}
	if e.stalePending {
		if e.staleSpoken++; e.staleSpoken > 2 { // it is not coming (the flush produced nothing)
			e.stalePending = false
		}
	}
	e.phrase.Reset()
	e.active, e.closed = true, false
	e.fresh, e.suspect = false, false
	e.lastShown, e.changedAt = "", now
}

// Partial reports whether the partial should be shown to the page.
func (e *Endpointer) Partial(text string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false // a late partial of a phrase already closed here
	}
	if !e.active {
		e.active, e.lastShown, e.changedAt = true, "", now
		e.fresh, e.suspect = false, false
	}
	if !e.fresh {
		if e.lastFinal != "" && sameText(normalize(text), e.lastFinal) {
			e.suspect = true // the previous phrase echoed by the worker
			return false
		}
		e.fresh = true
	}
	if !e.phrase.Partial(text) {
		return false
	}
	if text != e.lastShown {
		e.lastShown, e.changedAt = text, now
	}
	return true
}

// Final handles the worker's FINAL. send is false when the phrase was already closed here.
func (e *Endpointer) Final(text string) (out string, usedPartial, send bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		e.closed, e.stalePending = false, false
		return "", false, false
	}
	if e.stalePending {
		e.stalePending = false
		if n := normalize(text); n != "" && relatedToStale(n, e.staleText) {
			// the late final of the phrase closed before this one began: drop it, and the
			// phrase in progress goes on (its own final is still to come)
			return "", false, false
		}
	}
	e.active, e.lastShown = false, ""
	out, usedPartial = e.phrase.Final(text)
	if n := normalize(out); n != "" {
		e.lastFinal = n
		rememberSent(n)
	}
	return out, usedPartial, true
}

// Close is what Tick decided: Flush the worker; Text is the phrase to send (empty = noise).
type Close struct {
	Text  string
	Flush bool
}

// Tick closes the phrase in progress when its text has been still for Idle.
func (e *Endpointer) Tick(now time.Time) (Close, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Idle <= 0 || !e.active {
		return Close{}, false
	}
	// Before the first partial the model may simply be slow to answer (the CPU model needs
	// a couple of seconds to start on a cold phrase): wait twice as long before calling it noise.
	wait := e.Idle
	if e.lastShown == "" {
		wait = 2 * e.Idle
	}
	if now.Sub(e.changedAt) < wait {
		return Close{}, false
	}
	best := strings.TrimSpace(e.phrase.Best())
	e.phrase.Reset()
	e.active, e.lastShown = false, ""
	if best == "" && e.suspect {
		// Only the old phrase echoed: either nothing was said, or the speaker said the very
		// same thing again. Close the recording and let the worker's own final decide.
		return Close{Flush: true}, true
	}
	e.closed = true
	if isNoise(best) {
		e.staleText = "" // nothing was sent: there is no late final to recognise
		return Close{Flush: true}, true
	}
	e.lastFinal = normalize(best)
	e.staleText = e.lastFinal
	rememberSent(e.lastFinal)
	return Close{Text: best, Flush: true}, true
}

// relatedToStale tells whether a worker final (normalized) is the late final of the phrase
// that was already sent as s: the same words, or those words plus the ones the partials missed.
func relatedToStale(n, s string) bool {
	return n == s || strings.HasPrefix(n, s+" ") || strings.HasPrefix(s, n+" ") || sameText(n, s)
}

// sameText tells whether two normalized texts are the same phrase. The worker's echo of the
// previous phrase is its last partial, which is close to the final that was sent but rarely
// identical (an accent, a word). Short texts must match exactly; longer ones when most words
// line up in order.
func sameText(a, b string) bool {
	if a == b {
		return true
	}
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) < 3 || len(wb) < 3 {
		return false
	}
	// longest common subsequence of words
	prev := make([]int, len(wb)+1)
	for i := 1; i <= len(wa); i++ {
		cur := make([]int, len(wb)+1)
		for j := 1; j <= len(wb); j++ {
			if wa[i-1] == wb[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev = cur
	}
	return 2*float64(prev[len(wb)]) >= 0.7*float64(len(wa)+len(wb))
}

var accents = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "à", "a", "è", "e", "ì", "i", "ò", "o", "ù", "u")

func normalize(s string) string {
	s = accents.Replace(strings.ToLower(s))
	return strings.ToLower(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}), " "))
}

// fillers are what the model invents over silence or breathing.
var fillers = map[string]bool{
	"okay": true, "ok": true, "yeah": true, "yes": true, "uh": true, "um": true, "hmm": true, "mm": true,
	"oh": true, "ah": true, "you": true, "the": true, "so": true, "bye": true, "thanks": true, "eh": true,
}

func isNoise(text string) bool {
	f := strings.Fields(text)
	switch len(f) {
	case 0:
		return true
	case 1:
		return fillers[strings.ToLower(strings.Trim(f[0], ".,!?¡¿…\"'"))]
	}
	return false
}
