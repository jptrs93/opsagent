package logconsumer

import (
	"errors"
	"strings"
	"testing"
	"time"

	backendlog "github.com/jptrs93/opsagent/backend/lib/log"
)

func TestProcessRawBinaryLinesPreservesStreamAndPartialFinalLine(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	lines := make(chan rawBinaryLogLine, 2)
	if err := processRawBinaryLinesWithClock(
		strings.NewReader("first\nsecond"),
		backendlog.BinaryStreamStderr,
		lines,
		func() time.Time { return now },
	); err != nil {
		t.Fatalf("processing lines: %v", err)
	}
	close(lines)

	var got []rawBinaryLogLine
	for line := range lines {
		got = append(got, line)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if string(got[0].line) != "first\n" || string(got[1].line) != "second" {
		t.Fatalf("lines = %q, %q", got[0].line, got[1].line)
	}
	for i, line := range got {
		if line.stream != backendlog.BinaryStreamStderr {
			t.Errorf("line %d stream = %d, want stderr", i, line.stream)
		}
		if !line.t.Equal(now) {
			t.Errorf("line %d time = %v, want %v", i, line.t, now)
		}
	}
}

func TestProcessRawBinaryLinesSplitsOversizedLines(t *testing.T) {
	line := strings.Repeat("x", maxLineLen*2+10) + "\n"
	lines := make(chan rawBinaryLogLine, 4)
	if err := processRawBinaryLinesWithClock(
		strings.NewReader(line),
		backendlog.BinaryStreamStdout,
		lines,
		time.Now,
	); err != nil {
		t.Fatalf("processing lines: %v", err)
	}
	close(lines)

	var got []rawBinaryLogLine
	for l := range lines {
		got = append(got, l)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	var rejoined []byte
	for i, l := range got {
		if i < len(got)-1 && len(l.line) != maxLineLen {
			t.Errorf("chunk %d len = %d, want %d", i, len(l.line), maxLineLen)
		}
		rejoined = append(rejoined, l.line...)
	}
	if string(rejoined) != line {
		t.Fatalf("rejoined chunks do not match input (len %d vs %d)", len(rejoined), len(line))
	}
}

func TestProcessRawBinaryLinesReturnsReadFailure(t *testing.T) {
	wantErr := errors.New("read failed")
	lines := make(chan rawBinaryLogLine, 1)
	err := processRawBinaryLinesWithClock(errorReader{err: wantErr}, backendlog.BinaryStreamStdout, lines, time.Now)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
