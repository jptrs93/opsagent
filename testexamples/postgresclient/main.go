package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const appName = "postgresclient"

func main() {
	logger := logu.NewJSONLogger(os.Stdout, slog.LevelInfo)
	cfg := configFromEnv()
	logger.Info("postgresclient starting", "host", cfg.Host, "port", cfg.Port, "database", cfg.Database, "user", cfg.User)
	passwordHash := sha256.Sum256([]byte(cfg.Password))
	credentialFingerprint := fmt.Sprintf("%x", passwordHash[:6])

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var err error
	for attempt := 1; ; attempt++ {
		err = runOnce(ctx, cfg, logger)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			logger.Error("postgresclient failed", "err", err)
			os.Exit(1)
		}
		logger.Warn("postgresclient waiting for postgres", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			logger.Error("postgresclient failed", "err", err)
			os.Exit(1)
		case <-time.After(2 * time.Second):
		}
	}

	logger.Info("postgresclient connected credential", "sha256", credentialFingerprint)
	logger.Info("postgresclient completed database verification")
	for tick := 1; ; tick++ {
		logger.Info("postgresclient healthy", "tick", tick)
		time.Sleep(10 * time.Second)
	}
}

type pgConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
	Write    string
	Expect   []string
}

func configFromEnv() pgConfig {
	return pgConfig{
		Host:     envOr("PGHOST", "127.0.0.1"),
		Port:     envOr("PGPORT", "5432"),
		User:     envOr("PGUSER", "postgres"),
		Password: os.Getenv("PGPASSWORD"),
		Database: envOr("PGDATABASE", "postgres"),
		Write:    strings.TrimSpace(os.Getenv("OPENDEPLOY_E2E_WRITE")),
		Expect:   splitCSV(os.Getenv("OPENDEPLOY_E2E_EXPECT")),
	}
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func runOnce(ctx context.Context, cfg pgConfig, logger *slog.Logger) error {
	conn, err := dialPostgres(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if cfg.Write != "" || len(cfg.Expect) > 0 {
		return runPersistentDataset(conn, cfg, logger)
	}

	queries := []string{
		`CREATE TABLE IF NOT EXISTS opendeploy_e2e (id integer PRIMARY KEY, name text NOT NULL)`,
		`TRUNCATE TABLE opendeploy_e2e`,
		`INSERT INTO opendeploy_e2e (id, name) VALUES (1, 'alpha'), (2, 'bravo'), (3, 'charlie')`,
	}
	for _, query := range queries {
		if _, err := conn.Exec(query); err != nil {
			return err
		}
	}

	rows, err := conn.Query(`SELECT id, name FROM opendeploy_e2e ORDER BY id`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		logger.Info("postgresclient row", "id", row[0], "name", row[1])
	}
	if len(rows) != 3 || rows[0][1] != "alpha" || rows[1][1] != "bravo" || rows[2][1] != "charlie" {
		return fmt.Errorf("unexpected rows: %v", rows)
	}
	logger.Info("postgresclient verified rows", "count", len(rows))
	return nil
}

func runPersistentDataset(conn *pgConn, cfg pgConfig, logger *slog.Logger) error {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS opendeploy_pgbackrest_e2e (value text PRIMARY KEY)`); err != nil {
		return err
	}
	if cfg.Write != "" {
		query := `INSERT INTO opendeploy_pgbackrest_e2e (value) VALUES (` + sqlString(cfg.Write) + `) ON CONFLICT DO NOTHING`
		if _, err := conn.Exec(query); err != nil {
			return err
		}
		logger.Info("postgresclient wrote persistent value", "value", cfg.Write)
	}

	rows, err := conn.Query(`SELECT value FROM opendeploy_pgbackrest_e2e ORDER BY value`)
	if err != nil {
		return err
	}
	values := make(map[string]bool, len(rows))
	for _, row := range rows {
		if len(row) > 0 {
			values[row[0]] = true
			logger.Info("postgresclient persistent row", "value", row[0])
		}
	}
	for _, expected := range cfg.Expect {
		if !values[expected] {
			return fmt.Errorf("persistent value %q is missing from %v", expected, values)
		}
	}
	logger.Info("postgresclient verified persistent rows", "count", len(rows))
	return nil
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

type pgConn struct {
	c net.Conn
	r *bufio.Reader
}

func dialPostgres(ctx context.Context, cfg pgConfig) (*pgConn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, cfg.Port))
	if err != nil {
		return nil, err
	}
	conn := &pgConn{c: raw, r: bufio.NewReader(raw)}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	if err := conn.startup(cfg); err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return conn, nil
}

func (c *pgConn) Close() error {
	_ = writeTypedMessage(c.c, 'X', nil)
	return c.c.Close()
}

func (c *pgConn) startup(cfg pgConfig) error {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, int32(196608))
	for _, kv := range [][2]string{
		{"user", cfg.User},
		{"database", cfg.Database},
		{"application_name", appName},
		{"client_encoding", "UTF8"},
	} {
		body.WriteString(kv[0])
		body.WriteByte(0)
		body.WriteString(kv[1])
		body.WriteByte(0)
	}
	body.WriteByte(0)
	if err := writeStartupMessage(c.c, body.Bytes()); err != nil {
		return err
	}

	for {
		typ, msg, err := c.readMessage()
		if err != nil {
			return err
		}
		switch typ {
		case 'R':
			if err := c.handleAuth(msg, cfg); err != nil {
				return err
			}
		case 'S', 'K', 'N':
			// ParameterStatus, BackendKeyData, NoticeResponse.
		case 'E':
			return parseErrorResponse(msg)
		case 'Z':
			return nil
		default:
			return fmt.Errorf("unexpected startup message %q", typ)
		}
	}
}

func (c *pgConn) handleAuth(msg []byte, cfg pgConfig) error {
	if len(msg) < 4 {
		return errors.New("short auth message")
	}
	code := binary.BigEndian.Uint32(msg[:4])
	switch code {
	case 0:
		return nil
	case 3:
		return writePasswordMessage(c.c, cfg.Password)
	case 10:
		mechanisms := splitZeroStrings(msg[4:])
		if !contains(mechanisms, "SCRAM-SHA-256") {
			return fmt.Errorf("server offered unsupported SASL mechanisms: %v", mechanisms)
		}
		return c.scramAuth(cfg)
	default:
		return fmt.Errorf("unsupported postgres auth code %d", code)
	}
}

func (c *pgConn) scramAuth(cfg pgConfig) error {
	nonce, err := randomNonce()
	if err != nil {
		return err
	}
	clientFirstBare := "n=" + saslName(cfg.User) + ",r=" + nonce
	clientFirst := "n,," + clientFirstBare

	var initial bytes.Buffer
	initial.WriteString("SCRAM-SHA-256")
	initial.WriteByte(0)
	_ = binary.Write(&initial, binary.BigEndian, int32(len(clientFirst)))
	initial.WriteString(clientFirst)
	if err := writeTypedMessage(c.c, 'p', initial.Bytes()); err != nil {
		return err
	}

	typ, msg, err := c.readMessage()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseErrorResponse(msg)
	}
	if typ != 'R' || len(msg) < 4 || binary.BigEndian.Uint32(msg[:4]) != 11 {
		return fmt.Errorf("expected SCRAM continue, got %q", typ)
	}
	serverFirst := string(msg[4:])
	attrs := parseSCRAMAttrs(serverFirst)
	serverNonce := attrs["r"]
	if !strings.HasPrefix(serverNonce, nonce) {
		return errors.New("server SCRAM nonce does not extend client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	if err != nil {
		return fmt.Errorf("decode SCRAM salt: %w", err)
	}
	iterations, err := strconv.Atoi(attrs["i"])
	if err != nil || iterations <= 0 {
		return fmt.Errorf("invalid SCRAM iteration count %q", attrs["i"])
	}

	clientFinalNoProof := "c=biws,r=" + serverNonce
	authMessage := clientFirstBare + "," + serverFirst + "," + clientFinalNoProof
	salted := pbkdf2SHA256([]byte(cfg.Password), salt, iterations, 32)
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSignature := hmacSHA256(storedKey[:], []byte(authMessage))
	proof := xorBytes(clientKey, clientSignature)
	serverKey := hmacSHA256(salted, []byte("Server Key"))
	serverSignature := hmacSHA256(serverKey, []byte(authMessage))

	clientFinal := clientFinalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	if err := writeTypedMessage(c.c, 'p', []byte(clientFinal)); err != nil {
		return err
	}

	typ, msg, err = c.readMessage()
	if err != nil {
		return err
	}
	if typ == 'E' {
		return parseErrorResponse(msg)
	}
	if typ != 'R' || len(msg) < 4 || binary.BigEndian.Uint32(msg[:4]) != 12 {
		return fmt.Errorf("expected SCRAM final, got %q", typ)
	}
	finalAttrs := parseSCRAMAttrs(string(msg[4:]))
	gotSig, err := base64.StdEncoding.DecodeString(finalAttrs["v"])
	if err != nil {
		return fmt.Errorf("decode server SCRAM signature: %w", err)
	}
	if !hmac.Equal(gotSig, serverSignature) {
		return errors.New("server SCRAM signature mismatch")
	}
	return nil
}

func (c *pgConn) Exec(query string) (string, error) {
	if err := writeTypedMessage(c.c, 'Q', append([]byte(query), 0)); err != nil {
		return "", err
	}
	var command string
	for {
		typ, msg, err := c.readMessage()
		if err != nil {
			return "", err
		}
		switch typ {
		case 'C':
			command = strings.TrimRight(string(msg), "\x00")
		case 'Z':
			return command, nil
		case 'E':
			return "", parseErrorResponse(msg)
		case 'N':
			// NoticeResponse.
		default:
			return "", fmt.Errorf("unexpected exec message %q", typ)
		}
	}
}

func (c *pgConn) Query(query string) ([][]string, error) {
	if err := writeTypedMessage(c.c, 'Q', append([]byte(query), 0)); err != nil {
		return nil, err
	}
	var rows [][]string
	for {
		typ, msg, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		switch typ {
		case 'T', 'C', 'N':
			// RowDescription, CommandComplete, NoticeResponse.
		case 'D':
			row, err := parseDataRow(msg)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		case 'Z':
			return rows, nil
		case 'E':
			return nil, parseErrorResponse(msg)
		default:
			return nil, fmt.Errorf("unexpected query message %q", typ)
		}
	}
}

func (c *pgConn) readMessage() (byte, []byte, error) {
	typ, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(c.r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n < 4 {
		return 0, nil, fmt.Errorf("invalid message length %d", n)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return 0, nil, err
	}
	return typ, body, nil
}

func writeStartupMessage(w io.Writer, body []byte) error {
	var msg bytes.Buffer
	_ = binary.Write(&msg, binary.BigEndian, int32(len(body)+4))
	msg.Write(body)
	_, err := w.Write(msg.Bytes())
	return err
}

func writePasswordMessage(w io.Writer, password string) error {
	return writeTypedMessage(w, 'p', append([]byte(password), 0))
}

func writeTypedMessage(w io.Writer, typ byte, body []byte) error {
	var msg bytes.Buffer
	msg.WriteByte(typ)
	_ = binary.Write(&msg, binary.BigEndian, int32(len(body)+4))
	msg.Write(body)
	_, err := w.Write(msg.Bytes())
	return err
}

func parseDataRow(msg []byte) ([]string, error) {
	if len(msg) < 2 {
		return nil, errors.New("short DataRow")
	}
	count := int(binary.BigEndian.Uint16(msg[:2]))
	pos := 2
	row := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if pos+4 > len(msg) {
			return nil, errors.New("short DataRow column length")
		}
		n := int(int32(binary.BigEndian.Uint32(msg[pos : pos+4])))
		pos += 4
		if n < 0 {
			row = append(row, "")
			continue
		}
		if pos+n > len(msg) {
			return nil, errors.New("short DataRow column value")
		}
		row = append(row, string(msg[pos:pos+n]))
		pos += n
	}
	return row, nil
}

func parseErrorResponse(msg []byte) error {
	fields := map[byte]string{}
	for len(msg) > 0 && msg[0] != 0 {
		code := msg[0]
		msg = msg[1:]
		idx := bytes.IndexByte(msg, 0)
		if idx < 0 {
			break
		}
		fields[code] = string(msg[:idx])
		msg = msg[idx+1:]
	}
	if fields['M'] != "" {
		return errors.New(fields['M'])
	}
	return errors.New("postgres error")
}

func splitZeroStrings(b []byte) []string {
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			out = append(out, string(part))
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func randomNonce() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

func saslName(s string) string {
	s = strings.ReplaceAll(s, "=", "=3D")
	s = strings.ReplaceAll(s, ",", "=2C")
	return s
}

func parseSCRAMAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if len(part) >= 3 && part[1] == '=' {
			out[part[:1]] = part[2:]
		}
	}
	return out
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, numBlocks*hLen)
	for block := 1; block <= numBlocks; block++ {
		var intBlock [4]byte
		binary.BigEndian.PutUint32(intBlock[:], uint32(block))
		u := hmacSHA256(password, append(append([]byte{}, salt...), intBlock[:]...))
		t := append([]byte{}, u...)
		for i := 1; i < iterations; i++ {
			u = hmacSHA256(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
