// Package inproc lets a service that lives in the same process answer plain net/http client calls
// with no port: Register("aiapi", handler) makes http.Get("inproc://aiapi/path") run handler directly.
// Bodies stream (server-sent events work), unlike httptest.ResponseRecorder.
package inproc

import (
	"io"
	"net/http"
	"sync"
)

var (
	mu       sync.Mutex
	handlers = map[string]http.Handler{}
	hooked   = map[string]bool{}
)

// Register serves URLs of the form inproc://<name>/... from h. Calling it again for the same name
// replaces the handler.
func Register(name string, h http.Handler) {
	mu.Lock()
	defer mu.Unlock()
	handlers[name] = h
	if !hooked["inproc"] {
		hooked["inproc"] = true
		http.DefaultTransport.(*http.Transport).RegisterProtocol("inproc", transport{})
	}
}

type transport struct{}

func (transport) RoundTrip(r *http.Request) (*http.Response, error) {
	mu.Lock()
	h := handlers[r.URL.Host]
	mu.Unlock()
	if h == nil {
		return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{}, Body: io.NopCloser(http.NoBody), Request: r}, nil
	}
	// The handler sees the request as a server would: path and query only, body streamed.
	sreq := r.Clone(r.Context())
	sreq.RequestURI = r.URL.RequestURI()
	sreq.RemoteAddr = "inproc"
	if sreq.Body == nil {
		sreq.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	w := &writer{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer func() {
			if v := recover(); v != nil {
				pw.CloseWithError(io.ErrUnexpectedEOF)
			} else {
				pw.Close()
			}
			w.start(http.StatusOK) // a handler that wrote nothing still answers
		}()
		h.ServeHTTP(w, sreq)
	}()
	select {
	case <-w.ready:
	case <-r.Context().Done():
		pr.CloseWithError(r.Context().Err())
		return nil, r.Context().Err()
	}
	return &http.Response{
		Status: http.StatusText(w.status), StatusCode: w.status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.header.Clone(), Body: pr, Request: r, ContentLength: -1,
	}, nil
}

type writer struct {
	header http.Header
	pw     *io.PipeWriter
	once   sync.Once
	ready  chan struct{}
	status int
}

func (w *writer) Header() http.Header { return w.header }

func (w *writer) start(code int) {
	w.once.Do(func() {
		w.status = code
		close(w.ready)
	})
}

func (w *writer) WriteHeader(code int) { w.start(code) }

func (w *writer) Write(b []byte) (int, error) {
	w.start(http.StatusOK)
	return w.pw.Write(b)
}

// Flush is a no-op: every Write already reaches the reader.
func (w *writer) Flush() {}
