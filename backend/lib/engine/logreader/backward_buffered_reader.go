package logreader

import (
	"fmt"
	"io"
)

const backwardBufferSize = 256 * 1024

type backwardReaderAt interface {
	ReadAt([]byte, int64) (int, error)
}

type BackwardBufferedReader struct {
	r       backwardReaderAt
	buf     []byte
	bufFrom int64
	bufTo   int64
}

func NewBackwardBufferedReader(r backwardReaderAt) *BackwardBufferedReader {
	return &BackwardBufferedReader{r: r, buf: make([]byte, backwardBufferSize)}
}

func (r *BackwardBufferedReader) ReadAt(p []byte, off int64) error {
	if len(p) == 0 {
		return nil
	}
	if off < 0 {
		return fmt.Errorf("negative read offset %d", off)
	}
	if int64(len(p)) > int64(len(r.buf)) {
		return readAtFull(r.r, p, off)
	}
	end := off + int64(len(p))
	if off < r.bufFrom || end > r.bufTo {
		if err := r.fill(off, end); err != nil {
			return err
		}
	}
	copy(p, r.buf[off-r.bufFrom:end-r.bufFrom])
	return nil
}

func (r *BackwardBufferedReader) fill(off int64, end int64) error {
	bufSize := int64(len(r.buf))
	from := end - bufSize
	if from < 0 {
		from = 0
	}
	if off < from {
		from = off
	}
	readLen := int(end - from)
	if err := readAtFull(r.r, r.buf[:readLen], from); err != nil {
		return err
	}
	r.bufFrom = from
	r.bufTo = end
	return nil
}

func readAtFull(r backwardReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if err == nil && n == len(p) {
		return nil
	}
	if err == io.EOF && n == len(p) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
