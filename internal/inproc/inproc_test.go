package inproc

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRegisterServesPlainClientCalls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		w.Write(b)
	})
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{"data: 1\n\n", "data: 2\n\n"} {
			io.WriteString(w, l)
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("GET /empty", func(w http.ResponseWriter, r *http.Request) {})
	Register("t", mux)

	resp, err := http.Post("inproc://t/echo", "text/plain", strings.NewReader("hola"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted || string(b) != "hola" {
		t.Fatalf("echo: %d %q", resp.StatusCode, b)
	}

	resp, err = http.Get("inproc://t/stream")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			got = append(got, sc.Text())
		}
	}
	if len(got) != 2 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %v %v", got, resp.Header)
	}

	resp, err = http.Get("inproc://t/empty")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("empty: %v %v", resp, err)
	}
	resp, err = http.Get("inproc://nobody/x")
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown: %v %v", resp, err)
	}
}
