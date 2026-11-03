package stt

import (
	"testing"
	"time"
)

func at(s float64) time.Time { return time.Unix(0, 0).Add(time.Duration(s * float64(time.Second))) }

func TestEndpointerClosesAfterIdleWithBestPartial(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	e.Speech(at(0))
	e.Partial("¿A qué herramientas tienes acceso?", at(0.5))
	e.Partial("¿A qué herramientas tienes acceso en este proyecto?", at(1.0))
	if _, ok := e.Tick(at(2.9)); ok {
		t.Fatal("must wait 2 s after the last change (1.0 -> 3.0)")
	}
	c, ok := e.Tick(at(3.1))
	if !ok || !c.Flush || c.Text != "¿A qué herramientas tienes acceso en este proyecto?" {
		t.Fatalf("got %+v %v", c, ok)
	}
	// the worker's own (bad) final for that phrase is dropped
	if _, _, send := e.Final("Aquí herramientas."); send {
		t.Fatal("the worker's final after an endpointer close must be dropped")
	}
	// ...and only once
	e.Speech(at(10))
	e.Partial("hola", at(10.2))
	if out, _, send := e.Final("hola"); !send || out != "hola" {
		t.Fatalf("next phrase: %q %v", out, send)
	}
}

func TestEndpointerChangesResetTheClock(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	e.Speech(at(0))
	for i := 1; i <= 5; i++ { // text keeps changing for 5 s
		e.Partial("una frase que sigue creciendo "+string(rune('a'+i)), at(float64(i)))
		if _, ok := e.Tick(at(float64(i) + 0.5)); ok {
			t.Fatalf("closed while the text was still changing (i=%d)", i)
		}
	}
	// an identical repeat is not a change
	e.Partial("una frase que sigue creciendo f", at(6))
	if _, ok := e.Tick(at(7.1)); !ok {
		t.Fatal("repeats of the same text must not keep the phrase open")
	}
}

func TestEndpointerNoiseAndSilence(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	// speech detected but no text at all
	e.Speech(at(0))
	if _, ok := e.Tick(at(2.1)); ok {
		t.Fatal("with no partial yet the model may just be slow: wait twice as long")
	}
	if c, ok := e.Tick(at(4.1)); !ok || c.Text != "" || !c.Flush {
		t.Fatalf("no text: %+v %v", c, ok)
	}
	// a filler is noise
	e.Speech(at(5))
	e.Partial("Okay.", at(5.3))
	if c, ok := e.Tick(at(7.4)); !ok || c.Text != "" {
		t.Fatalf("filler: %+v %v", c, ok)
	}
	// one real word is not
	e.Speech(at(10))
	e.Partial("Detente.", at(10.3))
	if c, ok := e.Tick(at(12.4)); !ok || c.Text != "Detente." {
		t.Fatalf("one word: %+v %v", c, ok)
	}
}

// What the worker really does: after a phrase, the next recording starts by echoing it.
func TestEndpointerIgnoresTheOldPhraseEchoedByTheWorker(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	e.Speech(at(0))
	e.Partial("No preguntes qué puede hacer tu país", at(1))
	e.Final("No preguntes qué puede hacer tu país.") // worker's final, different punctuation
	e.Speech(at(3))
	for i, echo := range []string{"No preguntes qué puede hacer tu país.", "no preguntes, qué puede hacer tu país", "No preguntes qué puede hacer tu país"} {
		if e.Partial(echo, at(3.1+float64(i)*0.2)) {
			t.Fatalf("stale echo %d was shown", i)
		}
	}
	// the new phrase arrives; it is shown and is what gets closed
	if !e.Partial("Alles hat ein Ende.", at(4)) {
		t.Fatal("fresh text must be shown")
	}
	c, ok := e.Tick(at(6.1))
	if !ok || c.Text != "Alles hat ein Ende." {
		t.Fatalf("got %+v %v", c, ok)
	}
}

// Only the echo and then silence: nothing was said, or the same words were said again. The
// recording is closed but the worker's own final is not dropped.
func TestEndpointerEchoOnlyLeavesTheWorkerFinal(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	e.Speech(at(0))
	e.Partial("sí por favor", at(1))
	e.Final("Sí, por favor.")
	e.Speech(at(3))
	e.Partial("Sí por favor", at(3.2)) // echo (or the speaker repeating it)
	c, ok := e.Tick(at(7.1))           // no fresh text yet: twice the idle time
	if !ok || c.Text != "" || !c.Flush {
		t.Fatalf("got %+v %v", c, ok)
	}
	if out, _, send := e.Final("Sí, por favor."); !send || out == "" {
		t.Fatalf("the worker's final must still go through: %q %v", out, send)
	}
}

// The echo is the worker's last partial, close to the final that was sent but not identical.
func TestEndpointerEchoMatchIsFuzzyForLongPhrases(t *testing.T) {
	e := NewEndpointer(2 * time.Second)
	e.Speech(at(0))
	e.Partial("No preguntes qué puede hacer tu país por ti, pregunta qué puedes hacer tú por tu país.", at(1))
	e.Final("No preguntes qué puede hacer tu país por ti, pregunta qué puedes hacer tú por tu país.")
	e.Speech(at(5))
	if e.Partial("No preguntes que puede hacer tu país por ti, pregunta que puedes hacer tú.", at(5.2)) {
		t.Fatal("a near copy of the previous phrase was shown")
	}
	// different words are fresh
	if !e.Partial("Hola, ¿me oyes bien?", at(5.5)) {
		t.Fatal("a different phrase must be shown")
	}
	// short phrases are only equal when identical
	if sameText(normalize("sí por favor"), normalize("sí gracias")) {
		t.Fatal("short different phrases must not match")
	}
}

func TestEndpointerRemembersTheLastPhraseAcrossSessions(t *testing.T) {
	first := NewEndpointer(2 * time.Second)
	first.Speech(at(0))
	first.Partial("hasta luego amigos", at(1))
	first.Final("Hasta luego, amigos.")
	// the page reconnects: a new session, the worker still echoes the old phrase
	second := NewEndpointer(2 * time.Second)
	second.Speech(at(0))
	if second.Partial("Hasta luego amigos", at(0.3)) {
		t.Fatal("the echo of the previous session's last phrase was shown")
	}
}

func TestEndpointerOffAndIdleWithoutPhrase(t *testing.T) {
	if _, ok := NewEndpointer(0).Tick(at(100)); ok {
		t.Fatal("disabled endpointer must never close")
	}
	e := NewEndpointer(2 * time.Second)
	if _, ok := e.Tick(at(100)); ok {
		t.Fatal("nothing in progress, nothing to close")
	}
	e.Speech(at(0))
	e.Partial("hola buenos días", at(1))
	e.Final("hola buenos días") // normal worker final before the idle time
	if _, ok := e.Tick(at(60)); ok {
		t.Fatal("a phrase the worker already closed must not be closed again")
	}
}
