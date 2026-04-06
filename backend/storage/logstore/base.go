package logstore

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const (
	logFileMagic          = "OALG"
	logFileVersion uint32 = 1

	logFileHeaderSize   = 8
	logRecordHeaderSize = 8

	maxLogRecordPayloadSize = 64 << 20
)

var (
	ErrInvalidLogFileHeader      = errors.New("invalid storage log file header")
	ErrLogRecordChecksumMismatch = errors.New("storage log record checksum mismatch")
	ErrLogRecordTooLarge         = errors.New("storage log record too large")
)

// AppendOnlyLogFileWrapper stores items in an append only log written to a binary file.
// The storage format is a versioned file header followed by checksummed length-prefixed records.
type AppendOnlyLogFileWrapper struct {
	Path       string
	OpenFile   *os.File
	Count      atomic.Int32
	LastRecord []byte
	Mu         sync.RWMutex
}

func encodeItemRecord[T Encodable](item T) []byte {
	return encodePayloadRecord(item.Encode())
}

func encodePayloadRecord(payload []byte) []byte {
	record := make([]byte, logRecordHeaderSize+len(payload))
	binary.LittleEndian.PutUint32(record[:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(record[4:8], crc32.ChecksumIEEE(payload))
	copy(record[8:], payload)
	return record
}

func encodedRecordPayload(record []byte) ([]byte, error) {
	if len(record) < logRecordHeaderSize {
		return nil, fmt.Errorf("encoded record too short: %d", len(record))
	}
	payloadLen := binary.LittleEndian.Uint32(record[:4])
	if payloadLen > maxLogRecordPayloadSize {
		return nil, fmt.Errorf("%w: %d exceeds max %d", ErrLogRecordTooLarge, payloadLen, maxLogRecordPayloadSize)
	}
	if int(payloadLen) != len(record)-logRecordHeaderSize {
		return nil, fmt.Errorf("encoded record length mismatch: declared %d, actual %d", payloadLen, len(record)-logRecordHeaderSize)
	}
	return record[logRecordHeaderSize:], nil
}

func expectedFileHeader() [logFileHeaderSize]byte {
	var header [logFileHeaderSize]byte
	copy(header[:4], logFileMagic)
	binary.LittleEndian.PutUint32(header[4:], logFileVersion)
	return header
}

func ensureFileHeader(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stating '%v': %w", f.Name(), err)
	}

	if info.Size() == 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seeking '%v': %w", f.Name(), err)
		}
		header := expectedFileHeader()
		if err := writeAll(f, header[:]); err != nil {
			return fmt.Errorf("writing file header to '%v': %w", f.Name(), err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("syncing '%v': %w", f.Name(), err)
		}
		return nil
	}

	if info.Size() < logFileHeaderSize {
		return fmt.Errorf("%w: '%v' is shorter than %d bytes", ErrInvalidLogFileHeader, f.Name(), logFileHeaderSize)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seeking '%v': %w", f.Name(), err)
	}
	var header [logFileHeaderSize]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return fmt.Errorf("reading file header from '%v': %w", f.Name(), err)
	}
	expected := expectedFileHeader()
	if header != expected {
		return fmt.Errorf("%w: '%v' has unexpected magic/version", ErrInvalidLogFileHeader, f.Name())
	}
	return nil
}

func readFileHeader(reader io.Reader) error {
	var header [logFileHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: missing or truncated file header", ErrInvalidLogFileHeader)
		}
		return err
	}
	expected := expectedFileHeader()
	if header != expected {
		return fmt.Errorf("%w: unexpected magic/version", ErrInvalidLogFileHeader)
	}
	return nil
}

func readRecordPayload(reader io.Reader) ([]byte, error) {
	var recordHeader [logRecordHeaderSize]byte
	n, err := io.ReadFull(reader, recordHeader[:])
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}

	payloadLen := binary.LittleEndian.Uint32(recordHeader[:4])
	if payloadLen > maxLogRecordPayloadSize {
		return nil, fmt.Errorf("%w: %d exceeds max %d", ErrLogRecordTooLarge, payloadLen, maxLogRecordPayloadSize)
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}

	expectedChecksum := binary.LittleEndian.Uint32(recordHeader[4:8])
	if actualChecksum := crc32.ChecksumIEEE(payload); actualChecksum != expectedChecksum {
		return nil, fmt.Errorf("%w: expected %08x, got %08x", ErrLogRecordChecksumMismatch, expectedChecksum, actualChecksum)
	}
	return payload, nil
}

func (a *AppendOnlyLogFileWrapper) InitFile(ctx context.Context, indexer func([]byte)) error {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.initFile(ctx, indexer, false)
}

func (a *AppendOnlyLogFileWrapper) initFile(ctx context.Context, indexer func([]byte), panicOnCorruption bool) error {
	f, err := ensureOpenFile(a.Path)
	if err != nil {
		return err
	}
	if err := ensureFileHeader(f); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			slog.WarnContext(ctx, fmt.Sprintf("closing file after init failure: %v", closeErr))
		}
		return err
	}

	count, lastRecord, err := validateAndDeCorrupt(ctx, f, indexer, panicOnCorruption)
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			slog.WarnContext(ctx, fmt.Sprintf("closing file after init failure: %v", closeErr))
		}
		return err
	}
	a.OpenFile = f
	a.Count.Store(count)
	a.LastRecord = lastRecord
	return nil
}

func (a *AppendOnlyLogFileWrapper) Append(ctx context.Context, b []byte) error {
	payload, err := encodedRecordPayload(b)
	if err != nil {
		return err
	}

	a.Mu.Lock()
	defer a.Mu.Unlock()

	if a.OpenFile == nil {
		if err := a.initFile(ctx, nil, true); err != nil {
			return err
		}
	}

	if err := writeAll(a.OpenFile, b); err != nil {
		a.CloseFile(ctx)
		return err
	}

	if err := a.OpenFile.Sync(); err != nil {
		a.CloseFile(ctx)
		return err
	}

	a.Count.Add(1)
	a.LastRecord = append([]byte(nil), payload...)
	return nil
}

func (a *AppendOnlyLogFileWrapper) CloseFile(ctx context.Context) {
	if a.OpenFile == nil {
		return
	}
	closeErr := a.OpenFile.Close()
	if closeErr != nil {
		slog.WarnContext(ctx, fmt.Sprintf("closing file: %v", closeErr))
	}
	a.OpenFile = nil
}

func (a *AppendOnlyLogFileWrapper) Seq() iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		path := a.Path
		maxRecords := a.Count.Load()
		if maxRecords <= 0 {
			return
		}

		f, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			yield(nil, fmt.Errorf("opening '%v': %w", path, err))
			return
		}
		defer f.Close()
		reader := bufio.NewReader(f)

		if err := readFileHeader(reader); err != nil {
			yield(nil, fmt.Errorf("reading file header from '%v': %w", f.Name(), err))
			return
		}

		var readCount int32
		for readCount < maxRecords {
			record, err := readRecordPayload(reader)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return
				}
				yield(nil, fmt.Errorf("reading record payload from '%v': %w", f.Name(), err))
				return
			}

			readCount += 1
			if !yield(record, nil) {
				return
			}
		}
	}
}

func validateAndDeCorrupt(ctx context.Context, f *os.File, indexer func([]byte), panicOnCorruption bool) (int32, []byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, nil, fmt.Errorf("seeking '%v': %w", f.Name(), err)
	}
	reader := bufio.NewReader(f)
	if err := readFileHeader(reader); err != nil {
		return 0, nil, fmt.Errorf("reading file header from '%v': %w", f.Name(), err)
	}

	validatedBytes := int64(logFileHeaderSize)
	var lastRecord []byte
	var count int32
	for {
		record, err := readRecordPayload(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return count, lastRecord, nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				if panicOnCorruption {
					panic(fmt.Sprintf("storage log file '%v', crashing program", f.Name()))
				}
				slog.WarnContext(ctx, "file contains incomplete record at end, likely corruption, truncated partial record")
				if err := f.Truncate(validatedBytes); err != nil {
					return count, lastRecord, fmt.Errorf("truncating file: %v", err)
				}
				return count, lastRecord, nil
			}
			if errors.Is(err, ErrLogRecordChecksumMismatch) || errors.Is(err, ErrLogRecordTooLarge) {
				if panicOnCorruption {
					panic(fmt.Sprintf("storage log file '%v', crashing program", f.Name()))
				}
			}
			return count, lastRecord, fmt.Errorf("validating file '%v': %w", f.Name(), err)
		}

		lastRecord = record
		count += 1
		validatedBytes += logRecordHeaderSize + int64(len(record))
		if indexer != nil {
			indexer(record)
		}
	}
}

func writeAll(f *os.File, b []byte) error {
	totalWritten := 0
	for totalWritten < len(b) {
		n, err := f.Write(b[totalWritten:])
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		totalWritten += n
	}
	return nil
}

func ensureOpenFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening '%v': %w", path, err)
	}
	return f, nil
}
