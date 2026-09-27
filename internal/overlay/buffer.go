package overlay

import (
	"io"
	"net/http"
	"sync"
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
