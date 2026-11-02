package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ScanObjectReader returns bytes from the configured private object store,
// never from a client-supplied URL or a scanner-visible filesystem path.
type ScanObjectReader interface {
	OpenForScan(context.Context, string) (io.ReadCloser, error)
}

type ClamAVScanner struct {
	objects         ScanObjectReader
	socket          string
	timeout         time.Duration
	maxSignatureAge time.Duration
	clock           func() time.Time
}

// ClamD's protocol has no authentication/encryption. Use a private Unix socket
// shared only with the media process, rather than exposing a TCP listener.
// Protocol: https://docs.clamav.net/manual/Usage/ClamdProtocol.html
func NewClamAVScanner(objects ScanObjectReader, socket string, timeout time.Duration) (*ClamAVScanner, error) {
	return NewClamAVScannerWithFreshness(objects, socket, timeout, 48*time.Hour, time.Now)
}

// The daemon must use TZ=UTC: ClamD's version timestamp has no timezone suffix.
// Readiness and every scan check the loaded database, rather than file mtimes or
// freshclam's process status (which do not prove the daemon loaded new data).
func NewClamAVScannerWithFreshness(objects ScanObjectReader, socket string, timeout, maxSignatureAge time.Duration, clock func() time.Time) (*ClamAVScanner, error) {
	if objects == nil || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || len(socket) > 100 || strings.ContainsAny(socket, "\x00\r\n") || timeout < time.Second || timeout > time.Minute || maxSignatureAge < time.Hour || maxSignatureAge > 7*24*time.Hour || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ClamAVScanner{objects: objects, socket: socket, timeout: timeout, maxSignatureAge: maxSignatureAge, clock: clock}, nil
}

func (scanner *ClamAVScanner) connect(ctx context.Context) (net.Conn, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, scanner.timeout)
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", scanner.socket)
	if err != nil {
		cancel()
		return nil, func() {}, ErrDependency
	}
	deadline, _ := ctx.Deadline()
	if err = connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		cancel()
		return nil, func() {}, ErrDependency
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	return connection, func() { stop(); _ = connection.Close(); cancel() }, nil
}

func scanWriteAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func clamReply(reader io.Reader) (string, error) {
	value, err := bufio.NewReader(io.LimitReader(reader, 8193)).ReadString(0)
	if err != nil || len(value) > 8192 {
		return "", ErrDependency
	}
	return strings.TrimSuffix(value, "\x00"), nil
}

func (scanner *ClamAVScanner) Ready(ctx context.Context) error {
	connection, closeConnection, err := scanner.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConnection()
	if scanWriteAll(connection, []byte("zVERSIONCOMMANDS\x00")) != nil {
		return ErrDependency
	}
	reply, err := clamReply(connection)
	if err != nil || !strings.HasPrefix(reply, "ClamAV ") {
		return ErrDependency
	}
	version, commands, ok := strings.Cut(reply, "| COMMANDS:")
	if !ok {
		return ErrDependency
	}
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(version, "ClamAV ")), "/")
	if len(parts) != 3 || !validClamEngine(parts[0]) {
		return ErrDependency
	}
	database, parseErr := strconv.ParseUint(parts[1], 10, 64)
	if parseErr != nil || database == 0 {
		return ErrDependency
	}
	builtAt, parseErr := time.ParseInLocation("Mon Jan _2 15:04:05 2006", parts[2], time.UTC)
	now := scanner.clock().UTC()
	if parseErr != nil || builtAt.After(now.Add(5*time.Minute)) || now.Sub(builtAt) > scanner.maxSignatureAge {
		return ErrDependency
	}
	for _, command := range strings.Fields(commands) {
		if command == "INSTREAM" {
			return nil
		}
	}
	return ErrDependency
}

func validClamEngine(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 5 {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func (scanner *ClamAVScanner) Scan(ctx context.Context, key string) (ScanResult, error) {
	if !validObjectKey(key) {
		return ScanResult{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, scanner.timeout)
	defer cancel()
	if err := scanner.Ready(ctx); err != nil {
		return ScanResult{}, ErrDependency
	}
	object, err := scanner.objects.OpenForScan(ctx, key)
	if err != nil || object == nil {
		return ScanResult{}, ErrDependency
	}
	defer object.Close()
	connection, closeConnection, err := scanner.connect(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	defer closeConnection()
	if scanWriteAll(connection, []byte("zINSTREAM\x00")) != nil {
		return ScanResult{}, ErrDependency
	}
	buffer := make([]byte, 32*1024)
	var total int64
	var header [4]byte
	for {
		n, readErr := object.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > 15<<20 {
				return ScanResult{}, ErrDependency
			}
			binary.BigEndian.PutUint32(header[:], uint32(n))
			if scanWriteAll(connection, header[:]) != nil || scanWriteAll(connection, buffer[:n]) != nil {
				return ScanResult{}, ErrDependency
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ScanResult{}, ErrDependency
		}
	}
	if total == 0 || scanWriteAll(connection, []byte{0, 0, 0, 0}) != nil {
		return ScanResult{}, ErrDependency
	}
	reply, err := clamReply(connection)
	if err != nil {
		return ScanResult{}, err
	}
	if reply == "stream: OK" {
		if err := scanner.Ready(ctx); err != nil {
			return ScanResult{}, ErrDependency
		}
		return ScanResult{Clean: true}, nil
	}
	if strings.HasPrefix(reply, "stream: ") && strings.HasSuffix(reply, " FOUND") {
		return ScanResult{Clean: false, ReasonCode: "MALWARE_DETECTED"}, nil
	}
	return ScanResult{}, ErrDependency
}

var _ MalwareScanner = (*ClamAVScanner)(nil)
