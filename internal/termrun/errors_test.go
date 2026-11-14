package termrun

import (
	"strings"
	"testing"
)

func TestFindErrorsNewestFirstWithTheirTrace(t *testing.T) {
	out := "\x1b[32mcompiling\x1b[0m\r\n" +
		"progress 10%\rprogress 100%\r\n" +
		"src/a.ts(3,1): error TS2304: Cannot find name 'x'.\r\n" +
		"  3 | x()\r\n" +
		"    | ^\r\n" +
		"all good here\r\n" +
		"Found 0 errors.\r\n" +
		"panic: boom\r\n" +
		"goroutine 1 [running]:\r\n" +
		"main.main()\r\n" +
		"\t/app/main.go:9 +0x1d\r\n" +
		"exit status 2\r\n"
	got := FindErrors(out, 0)
	if len(got) != 2 {
		t.Fatalf("got %d blocks: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Text, "panic: boom") || !strings.HasPrefix(got[1].Text, "src/a.ts") {
		t.Fatalf("order or content wrong: %+v", got)
	}
	if !strings.Contains(got[0].Text, "main.go:9") || !strings.Contains(got[0].Text, "exit status 2") || !strings.Contains(got[1].Text, "^") {
		t.Fatalf("the trace was cut: %+v", got)
	}
	for _, b := range got {
		if strings.Contains(b.Text, "0 errors") || strings.Contains(b.Text, "\x1b") {
			t.Fatalf("clean line or escape code in %+v", b)
		}
	}
	if one := FindErrors(out, 1); len(one) != 1 || !strings.HasPrefix(one[0].Text, "panic: boom") {
		t.Fatalf("limit: %+v", one)
	}
}

func TestCleanTextKeepsTheLastRedraw(t *testing.T) {
	if got := CleanText("a 1%\ra 50%\ra 100%\nnext"); got != "a 100%\nnext" {
		t.Fatalf("%q", got)
	}
	if got := Tail("1\n2\n3\n4\n", 2); got != "3\n4" {
		t.Fatalf("tail %q", got)
	}
}
