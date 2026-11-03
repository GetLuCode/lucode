package stt

import "testing"

// The real case from the worker log: good partials, then silence artifacts, then a bad final.
func TestPhraseKeepsFullestPartialOverBadFinal(t *testing.T) {
	var p Phrase
	shown := []string{}
	for _, part := range []string{
		"¿A qué herramientas tiene?",
		"¿A qué herramientas tienes acceso?",
		"¿A qué herramientas tienes acceso en este proyecto?",
		"Okay.", // hallucinated over the trailing silence
		"Aquí herramienta.",
	} {
		if p.Partial(part) {
			shown = append(shown, part)
		}
	}
	if len(shown) != 3 {
		t.Fatalf("degraded partials must not be shown, got %v", shown)
	}
	out, usedPartial := p.Final("Aquí herramientas.")
	if !usedPartial || out != "¿A qué herramientas tienes acceso en este proyecto?" {
		t.Fatalf("got %q (partial=%v)", out, usedPartial)
	}
}

func TestPhrasePrefersGoodFinal(t *testing.T) {
	var p Phrase
	p.Partial("No preguntes qué puede hacer tu país por ti, pregunta qué puedes hacer tú por tu país.")
	out, usedPartial := p.Final("No preguntes qué puede hacer tu país por ti, pregunta qué puedes hacer tú por tu país.")
	if usedPartial || out == "" {
		t.Fatalf("a final as long as the partials is kept: %q %v", out, usedPartial)
	}
	// a longer final (the model heard more at the end) also wins
	p.Partial("hola qué tal")
	out, usedPartial = p.Final("hola qué tal estás hoy")
	if usedPartial || out != "hola qué tal estás hoy" {
		t.Fatalf("got %q", out)
	}
}

func TestPhraseNoiseDoesNotBecomeText(t *testing.T) {
	var p Phrase
	p.Partial("Okay.")
	out, usedPartial := p.Final("")
	if usedPartial || out != "" {
		t.Fatalf("a one-word hallucination must not replace an empty final: %q", out)
	}
}

func TestPhraseResets(t *testing.T) {
	var p Phrase
	p.Partial("una frase larga de prueba aquí")
	p.Final("una frase larga de prueba aquí")
	if out, used := p.Final("hola"); used || out != "hola" {
		t.Fatalf("the next phrase must not inherit the previous partials: %q", out)
	}
}
