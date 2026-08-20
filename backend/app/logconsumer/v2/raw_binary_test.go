package logconsumer

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type sunkLine struct {
	t    time.Time
	line string
}

func collectSink(got *[]sunkLine) func(time.Time, []byte) {
	return func(t time.Time, line []byte) {
		*got = append(*got, sunkLine{t: t, line: string(line)})
	}
}

func TestConsumeStreamPreservesPartialFinalLine(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	var got []sunkLine
	if err := consumeStream(
		strings.NewReader("first\nsecond"),
		collectSink(&got),
		func() time.Time { return now },
	); err != nil {
		t.Fatalf("consuming stream: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if got[0].line != "first\n" || got[1].line != "second" {
		t.Fatalf("lines = %q, %q", got[0].line, got[1].line)
	}
	for i, l := range got {
		if !l.t.Equal(now) {
			t.Errorf("line %d time = %v, want %v", i, l.t, now)
		}
	}
}

func TestConsumeStreamSplitsOversizedLines(t *testing.T) {
	line := strings.Repeat("x", maxLineLen*2+10) + "\n"
	var got []sunkLine
	if err := consumeStream(
		strings.NewReader(line),
		collectSink(&got),
		time.Now,
	); err != nil {
		t.Fatalf("consuming stream: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	var rejoined string
	for i, l := range got {
		if i < len(got)-1 && len(l.line) != maxLineLen {
			t.Errorf("chunk %d len = %d, want %d", i, len(l.line), maxLineLen)
		}
		rejoined += l.line
	}
	if rejoined != line {
		t.Fatalf("rejoined chunks do not match input (len %d vs %d)", len(rejoined), len(line))
	}
}

func TestConsumeStreamReturnsReadFailure(t *testing.T) {
	wantErr := errors.New("read failed")
	err := consumeStream(errorReader{err: wantErr}, func(time.Time, []byte) {}, time.Now)
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
