package metricstore

import (
	"encoding/binary"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const (
	frameLenSize    = 4
	frameCRCSize    = 4
	maxFramePayload = 1 << 20
)

func appendFrame(buf, payload []byte) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	return binary.BigEndian.AppendUint32(buf, logv2.PayloadCRC(payload))
}

type walWriter[S any] struct {
	dir    string
	encode func(*S) []byte
	timeOf func(*S) int64
	day    time.Time
	file   *os.File
}

func newWALWriter[S any, R any](t *table[S, R], dir string) *walWriter[S] {
	return &walWriter[S]{dir: dir, encode: t.encode, timeOf: t.timeOf}
}

func (w *walWriter[S]) append(samples []*S) error {
	var buf []byte
	for _, s := range samples {
		day := utcDay(time.UnixMilli(w.timeOf(s)))
		if w.file == nil || !day.Equal(w.day) {
			if err := w.flush(buf); err != nil {
				return err
			}
			buf = buf[:0]
			if err := w.open(day); err != nil {
				return err
			}
		}
		buf = appendFrame(buf, w.encode(s))
	}
	return w.flush(buf)
}

func (w *walWriter[S]) flush(buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	_, err := w.file.Write(buf)
	return err
}

func (w *walWriter[S]) open(day time.Time) error {
	if err := w.close(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.dir, 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(walPath(w.dir, day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w.file = f
	w.day = day
	return nil
}

func (w *walWriter[S]) close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func readWAL[S any](path string, decode func([]byte) (*S, error), accept func(payload []byte) bool, yield func(*S) bool) (records int, clean bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	records, clean = walkWAL(b, decode, accept, yield)
	return records, clean, nil
}

func walkWAL[S any](b []byte, decode func([]byte) (*S, error), accept func(payload []byte) bool, yield func(*S) bool) (records int, clean bool) {
	clean = walkFrames(b, func(payload []byte) bool {
		if accept != nil && !accept(payload) {
			return true
		}
		s, err := decode(payload)
		if err != nil {
			return true
		}
		records++
		return yield(s)
	})
	return records, clean
}

func walkFrames(b []byte, visit func(payload []byte) bool) (clean bool) {
	for len(b) > 0 {
		if len(b) < frameLenSize {
			return false
		}
		n := int(binary.BigEndian.Uint32(b))
		if n > maxFramePayload || len(b) < frameLenSize+n+frameCRCSize {
			return false
		}
		payload := b[frameLenSize : frameLenSize+n]
		if binary.BigEndian.Uint32(b[frameLenSize+n:]) != logv2.PayloadCRC(payload) {
			return false
		}
		b = b[frameLenSize+n+frameCRCSize:]
		if !visit(payload) {
			return true
		}
	}
	return true
}

func peekSample(payload []byte) (t int64, deploymentID int32) {
	b := payload
	for len(b) > 0 {
		var num apigen.Number
		var typ apigen.Type
		var err error
		b, num, typ, err = apigen.ConsumeTag(b)
		if err != nil {
			return t, deploymentID
		}
		switch num {
		case 1:
			b, t, err = apigen.ConsumeVarInt64(b, typ)
		case 2:
			_, deploymentID, _ = apigen.ConsumeVarInt32(b, typ)
			return t, deploymentID
		default:
			return t, deploymentID
		}
		if err != nil {
			return t, deploymentID
		}
	}
	return t, deploymentID
}
