package termrun

import (
	"regexp"
	"strings"
)

// A pseudo-terminal has ONE output: the process believes it is writing to a screen, and what it
// sends to stderr and to stdout arrives mixed. So "the errors" of a command cannot be read from a
// stderr stream; they are recognised in the text, by the words and shapes tools use to report them.
// The match is a heuristic: it finds what looks like an error, and a program that fails in silence
// or in an unusual way shows only through its exit code.

var (
	ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[()][A-Za-z0-9]|\x1b[=>]")
	// words that announce a problem
	errorRe = regexp.MustCompile(`(?i)\b(error|errors|failed|failure|fatal|panic|exception|traceback|segmentation fault|unhandled|denied|refused|cannot|can't|could not|couldn't|unable to|not found|no such file|undefined|ELIFECYCLE|ERR!|FAIL)\b|^\s*(E\d{3,5}|TS\d{4}|error\[E\d+\])|exit status [1-9]|exit code [1-9]|✗|✘|❌`)
	// "0 errors", "no errors": a clean report is not an error
	cleanRe = regexp.MustCompile(`(?i)\b(0|no|without|zero)\s+(errors?|failures?|failed)\b|\berrors?:\s*0\b`)
	// the lines that belong to an error above them: a stack trace, a source excerpt, a caret
	contRe = regexp.MustCompile(`^(\s+\S|goroutine \d+ |[\w./*\[\]-]+\(.*\)$|created by |Caused by|\s*at\s|\s*\^|\s*\|\s|\s*-->|\s*File ".*", line \d+|\s*\d+\s*\||.*\.(go|ts|tsx|js|jsx|py|rs|java|cs|rb|php|c|cc|cpp|h):\d+)`)
)

const (
	maxBlockLines = 14
	maxLineLen    = 400
)

// CleanText makes terminal output readable: escape sequences go, and a line that was redrawn with
// carriage returns (a progress bar) is kept as its last drawing.
func CleanText(raw string) string {
	raw = ansiRe.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		l = strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' {
				return -1
			}
			return r
		}, l)
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// ErrorBlock is one error found in the output: the line that announced it and what follows it.
type ErrorBlock struct {
	Line  int    `json:"line"` // 1-based line of the cleaned output where it starts
	Text  string `json:"text"`
	Lines int    `json:"lines"`
}

// FindErrors returns the error blocks of the output, the most recent first, at most limit
// (0 = all). A block is a line that looks like an error plus the lines that continue it.
func FindErrors(raw string, limit int) []ErrorBlock {
	lines := strings.Split(CleanText(raw), "\n")
	var blocks []ErrorBlock
	for i := 0; i < len(lines); {
		l := lines[i]
		if strings.TrimSpace(l) == "" || !errorRe.MatchString(l) || cleanRe.MatchString(l) {
			i++
			continue
		}
		end := i + 1
		for end < len(lines) && end-i < maxBlockLines && strings.TrimSpace(lines[end]) != "" && (contRe.MatchString(lines[end]) || errorRe.MatchString(lines[end])) {
			end++
		}
		text := make([]string, 0, end-i)
		for _, bl := range lines[i:end] {
			if len(bl) > maxLineLen {
				bl = bl[:maxLineLen] + "…"
			}
			text = append(text, bl)
		}
		blocks = append(blocks, ErrorBlock{Line: i + 1, Text: strings.Join(text, "\n"), Lines: end - i})
		i = end
	}
	// most recent first
	for a, b := 0, len(blocks)-1; a < b; a, b = a+1, b-1 {
		blocks[a], blocks[b] = blocks[b], blocks[a]
	}
	if limit > 0 && len(blocks) > limit {
		blocks = blocks[:limit]
	}
	return blocks
}

// Tail is the last n lines of the cleaned output.
func Tail(raw string, n int) string {
	// a terminal pads its output with blank lines: runs of them become one, and the ends go
	var lines []string
	for _, l := range strings.Split(CleanText(raw), "\n") {
		if strings.TrimSpace(l) == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, strings.TrimSpace(l))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
