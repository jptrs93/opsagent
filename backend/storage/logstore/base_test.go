package logstore

import (
	"context"
	"encoding/binary"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"testing"
)

type testTupleItem string

func (t testTupleItem) Encode() []byte {
	return []byte(string(t))
}

func decodeTestTupleItem(b []byte) (testTupleItem, error) {
	return testTupleItem(string(b)), nil
}

func writeTestLogFile(t *testing.T, path string, chunks ...[]byte) {
	t.Helper()

	combined := make([]byte, 0)
	for _, chunk := range chunks {
		combined = append(combined, chunk...)
	}
	if err := os.WriteFile(path, combined, 0o644); err != nil {
		t.Fatalf("writing test log file: %v", err)
	}
}

func fileHeaderBytes() []byte {
	header := expectedFileHeader()
	return append([]byte(nil), header[:]...)
}

func collectSeq(t *testing.T, seq iter.Seq2[[]byte, error]) [][]byte {
	t.Helper()

	items := make([][]byte, 0)
	for item, err := range seq {
		if err != nil {
			t.Fatalf("reading seq: %v", err)
		}
		items = append(items, append([]byte(nil), item...))
	}
	return items
}

func TestAppendOnlyLogFileWrapperRoundTripWithChecksum(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	payload := []byte("hello world")

	writer := &AppendOnlyLogFileWrapper{Path: path}
	if err := writer.Append(ctx, encodePayloadRecord(payload)); err != nil {
		t.Fatalf("append record: %v", err)
	}
	writer.CloseFile(ctx)

	var indexed [][]byte
	reader := &AppendOnlyLogFileWrapper{Path: path}
	if err := reader.InitFile(ctx, func(record []byte) {
		indexed = append(indexed, append([]byte(nil), record...))
	}); err != nil {
		t.Fatalf("init file: %v", err)
	}

	if got := reader.Count.Load(); got != 1 {
		t.Fatalf("record count = %d, want 1", got)
	}
	if string(reader.LastRecord) != string(payload) {
		t.Fatalf("last record = %q, want %q", reader.LastRecord, payload)
	}
	if len(indexed) != 1 || string(indexed[0]) != string(payload) {
		t.Fatalf("indexed records = %q, want %q", indexed, payload)
	}

	records := collectSeq(t, reader.Seq())
	if len(records) != 1 || string(records[0]) != string(payload) {
		t.Fatalf("seq records = %q, want %q", records, payload)
	}
}

func TestInitFileTruncatesIncompleteTrailingHeader(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	validRecord := encodePayloadRecord([]byte("first"))
	writeTestLogFile(t, path, fileHeaderBytes(), validRecord, []byte{0x01, 0x02, 0x03})

	store := &AppendOnlyLogFileWrapper{Path: path}
	if err := store.InitFile(ctx, nil); err != nil {
		t.Fatalf("init file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	wantSize := int64(logFileHeaderSize + len(validRecord))
	if info.Size() != wantSize {
		t.Fatalf("file size = %d, want %d", info.Size(), wantSize)
	}
	if got := store.Count.Load(); got != 1 {
		t.Fatalf("record count = %d, want 1", got)
	}
}

func TestInitFileTruncatesIncompleteTrailingPayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	validRecord := encodePayloadRecord([]byte("first"))
	truncatedRecord := encodePayloadRecord([]byte("second"))[:logRecordHeaderSize+2]
	writeTestLogFile(t, path, fileHeaderBytes(), validRecord, truncatedRecord)

	store := &AppendOnlyLogFileWrapper{Path: path}
	if err := store.InitFile(ctx, nil); err != nil {
		t.Fatalf("init file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	wantSize := int64(logFileHeaderSize + len(validRecord))
	if info.Size() != wantSize {
		t.Fatalf("file size = %d, want %d", info.Size(), wantSize)
	}
	if got := store.Count.Load(); got != 1 {
		t.Fatalf("record count = %d, want 1", got)
	}
}

func TestInitFileRejectsChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	badRecord := encodePayloadRecord([]byte("bad"))
	badRecord[len(badRecord)-1] ^= 0xff
	writeTestLogFile(t, path, fileHeaderBytes(), badRecord)

	store := &AppendOnlyLogFileWrapper{Path: path}
	err := store.InitFile(ctx, nil)
	if !errors.Is(err, ErrLogRecordChecksumMismatch) {
		t.Fatalf("InitFile error = %v, want checksum mismatch", err)
	}
}

func TestInitFileRejectsInvalidHeader(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	writeTestLogFile(t, path, []byte("notalog!"))

	store := &AppendOnlyLogFileWrapper{Path: path}
	err := store.InitFile(ctx, nil)
	if !errors.Is(err, ErrInvalidLogFileHeader) {
		t.Fatalf("InitFile error = %v, want invalid header", err)
	}
}

func TestInitFileRejectsOversizedRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.log.bin")
	var oversizedHeader [logRecordHeaderSize]byte
	binary.LittleEndian.PutUint32(oversizedHeader[:4], maxLogRecordPayloadSize+1)
	writeTestLogFile(t, path, fileHeaderBytes(), oversizedHeader[:])

	store := &AppendOnlyLogFileWrapper{Path: path}
	err := store.InitFile(ctx, nil)
	if !errors.Is(err, ErrLogRecordTooLarge) {
		t.Fatalf("InitFile error = %v, want oversized record", err)
	}
}

func TestTupleLogStoreInitUsesPayloadLastRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tuple.log.bin")

	store := New[testTupleItem](path, decodeTestTupleItem)
	if err := store.Write(ctx, testTupleItem("latest")); err != nil {
		t.Fatalf("write tuple record: %v", err)
	}
	store.Data.CloseFile(ctx)

	reopened, err := NewAndInit[testTupleItem](ctx, path, decodeTestTupleItem)
	if err != nil {
		t.Fatalf("reopen tuple store: %v", err)
	}
	if reopened.Latest != testTupleItem("latest") {
		t.Fatalf("latest = %q, want %q", reopened.Latest, testTupleItem("latest"))
	}
}
