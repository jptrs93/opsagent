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

type walWriter struct {
	dir  string
	day  time.Time
	file *os.File
}

func (w *walWriter) append(samples []*apigen.MetricsSample) error {
	var buf []byte
	for _, s := range samples {
		day := utcDay(time.UnixMilli(s.Time))
		if w.file == nil || !day.Equal(w.day) {
			if err := w.flush(buf); err != nil {
				return err
			}
			buf = buf[:0]
			if err := w.open(day); err != nil {
				return err
			}
		}
		buf = appendFrame(buf, s.Encode())
	}
	return w.flush(buf)
}

func (w *walWriter) flush(buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	_, err := w.file.Write(buf)
	return err
}

func (w *walWriter) open(day time.Time) error {
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

func (w *walWriter) close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func readWAL(path string, accept func(payload []byte) bool, yield func(*apigen.MetricsSample) bool) (records int, clean bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	for len(b) > 0 {
		if len(b) < frameLenSize {
			return records, false, nil
		}
		n := int(binary.BigEndian.Uint32(b))
		if n > maxFramePayload || len(b) < frameLenSize+n+frameCRCSize {
			return records, false, nil
		}
		payload := b[frameLenSize : frameLenSize+n]
		if binary.BigEndian.Uint32(b[frameLenSize+n:]) != logv2.PayloadCRC(payload) {
			return records, false, nil
		}
		b = b[frameLenSize+n+frameCRCSize:]
		if accept != nil && !accept(payload) {
			continue
		}
		s, err := apigen.DecodeMetricsSample(payload)
		if err != nil {
			continue
		}
		records++
		if !yield(s) {
			return records, true, nil
		}
	}
	return records, true, nil
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
