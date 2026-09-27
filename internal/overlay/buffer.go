package overlay

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

const copyBufferSize = 32 << 10

var buffers = sync.Pool{New: func() any {
	buffer := make([]byte, copyBufferSize)
	return &buffer
}}

// copyFlushing copies src to an HTTP response and flushes after every chunk, so a relay adds no buffering
// delay to streamed responses.
func copyFlushing(w io.Writer, controller *http.ResponseController, src io.Reader) error {
	buffer := buffers.Get().(*[]byte)
	defer buffers.Put(buffer)
	for {
		n, readErr := src.Read(*buffer)
		if n > 0 {
			if _, err := w.Write((*buffer)[:n]); err != nil {
				return err
			}
			if err := controller.Flush(); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// copyPooled copies src to dst with a pooled buffer.
func copyPooled(dst io.Writer, src io.Reader) error {
	buffer := buffers.Get().(*[]byte)
	defer buffers.Put(buffer)
	_, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *buffer)
	return err
}

// countingReader and countingWriter tally the bytes a relay copies through a tunnel, so its StageEnded event
// can report them. They are only used when the tunnel logs events (P7): the common case, with logging off,
// copies through copyPooled/copyFlushing directly and pays nothing for counting.
type countingReader struct {
	io.Reader
	n *atomic.Uint64
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.n.Add(uint64(n))
	}
	return n, err
}

type countingWriter struct {
	io.Writer
	n *atomic.Uint64
}

func (w countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.n.Add(uint64(n))
	}
	return n, err
}
