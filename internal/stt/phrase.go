package stt

import "strings"

// Phrase guards the transcripts of one spoken phrase against a bad final pass.
//
// The worker (RealtimeSTT) transcribes the growing phrase while it is spoken (partials,
// accurate) and, when the VAD closes it, transcribes the whole recording again for the
// FINAL. With the small int8 model that second pass can go wrong when the recording
// carries seconds of trailing silence: it answered "Aquí herramientas." for a phrase
// whose partials had read "¿A qué herramientas tienes acceso en este proyecto?", and the
// silence itself produced partials such as "Okay.". Phrase keeps the fullest partial and
// uses it when the final is clearly worse.
type Phrase struct {
	best string // the fullest partial heard in this phrase
}

func words(s string) int { return len(strings.Fields(s)) }

// Best is the fullest partial heard in this phrase.
func (p *Phrase) Best() string { return p.best }

// Reset starts a new phrase (call it on SPEECH_START and after each FINAL).
func (p *Phrase) Reset() { p.best = "" }

// Partial reports whether a partial should be shown. One far shorter than the best so far
// is the model degrading over silence, not the speaker taking words back.
func (p *Phrase) Partial(text string) bool {
	n, b := words(text), words(p.best)
	if b >= 4 && n*2 < b {
		return false
	}
	if n >= b {
		p.best = text
	}
	return true
}

// Final returns the text to commit for the phrase and starts the next one. The worker's
// final wins unless it is much shorter than the fullest partial (or empty while the
// partials clearly had speech).
func (p *Phrase) Final(text string) (out string, usedPartial bool) {
	best := p.best
	p.Reset()
	f, b := words(text), words(best)
	switch {
	case b >= 2 && f*10 < b*7: // under 70 % of the words heard while speaking
		return best, true
	}
	return text, false
}
