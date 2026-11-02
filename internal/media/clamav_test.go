package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const clamTestKey = "tenants/tenant-one/owners/owner-one/media/asset-one"

type clamObjects struct {
	body []byte
	fail bool
}

func (objects clamObjects) OpenForScan(ctx context.Context, key string) (io.ReadCloser, error) {
	if objects.fail || ctx.Err() != nil {
		return nil, ErrDependency
	}
	return io.NopCloser(bytes.NewReader(objects.body)), nil
}

func clamSocketTest(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "clam.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		handler(connection)
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return socket
}

type clamBufferedConn struct {
	net.Conn
	reader io.Reader
}

func (connection clamBufferedConn) Read(data []byte) (int, error) {
	return connection.reader.Read(data)
}

// A scan opens separate protocol/definition checks before and after clean
// bytes. The fixture models a persistent daemon rather than skipping checks.
func clamScanSocketTest(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "clam.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
				reader := bufio.NewReader(connection)
				command, err := reader.ReadString(0)
				if err != nil {
					return
				}
				if command == "zVERSIONCOMMANDS\x00" {
					_, _ = fmt.Fprintf(connection, "ClamAV 1.4.6/28163/%s | COMMANDS: PING INSTREAM VERSIONCOMMANDS\x00", time.Now().UTC().Add(-time.Hour).Format("Mon Jan _2 15:04:05 2006"))
					return
				}
				handler(clamBufferedConn{Conn: connection, reader: io.MultiReader(strings.NewReader(command), reader)})
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return socket
}

func TestClamAVFramingAndVerdicts(t *testing.T) {
	for name, tc := range map[string]struct {
		reply       string
		clean       bool
		malware     bool
		unavailable bool
	}{
		"clean":            {"stream: OK\x00", true, false, false},
		"malware":          {"stream: Eicar-Test-Signature FOUND\x00", false, true, false},
		"scan limit":       {"INSTREAM size limit exceeded. ERROR\x00", false, false, true},
		"unknown result":   {"stream: skipped\x00", false, false, true},
		"partial result":   {"stream: OK", false, false, true},
		"oversized result": {strings.Repeat("x", 8193) + "\x00", false, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("private document"), 5000)
			socket := clamScanSocketTest(t, func(connection net.Conn) {
				reader := bufio.NewReader(connection)
				command, err := reader.ReadString(0)
				if err != nil || command != "zINSTREAM\x00" {
					t.Error("unexpected command framing")
					return
				}
				var received []byte
				for {
					var size uint32
					if binary.Read(reader, binary.BigEndian, &size) != nil {
						t.Error("truncated scan framing")
						return
					}
					if size == 0 {
						break
					}
					if size > 32*1024 {
						t.Error("unbounded chunk")
						return
					}
					chunk := make([]byte, size)
					if _, err = io.ReadFull(reader, chunk); err != nil {
						t.Error(err)
						return
					}
					received = append(received, chunk...)
				}
				if !bytes.Equal(received, payload) {
					t.Error("scanner received different bytes")
				}
				_, _ = io.WriteString(connection, tc.reply)
			})
			scanner, err := NewClamAVScanner(clamObjects{body: payload}, socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result, err := scanner.Scan(context.Background(), clamTestKey)
			if tc.unavailable {
				if !errors.Is(err, ErrDependency) || result.Clean {
					t.Fatalf("failed scan became clean: %+v %v", result, err)
				}
				return
			}
			if err != nil || result.Clean != tc.clean || (tc.malware && result.ReasonCode != "MALWARE_DETECTED") {
				t.Fatalf("verdict=%+v %v", result, err)
			}
		})
	}
}

func TestClamAVReadinessRequiresSupportedStreamProtocol(t *testing.T) {
	date := time.Now().UTC().Add(-time.Hour).Format("Mon Jan _2 15:04:05 2006")
	for _, reply := range []string{"ClamAV 1.4.6/1/" + date + " | COMMANDS: PING INSTREAM VERSIONCOMMANDS\x00", "ClamAV 1.4.6/1/" + date + " | COMMANDS: PING\x00", "UNKNOWN COMMAND\x00"} {
		socket := clamSocketTest(t, func(connection net.Conn) {
			command, _ := bufio.NewReader(connection).ReadString(0)
			if command != "zVERSIONCOMMANDS\x00" {
				t.Error("unexpected readiness command")
			}
			_, _ = io.WriteString(connection, reply)
		})
		scanner, _ := NewClamAVScanner(clamObjects{}, socket, time.Second)
		err := scanner.Ready(context.Background())
		if strings.Contains(reply, " INSTREAM ") {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrDependency) {
			t.Fatal("incompatible scanner became ready")
		}
	}
}

func TestClamAVFailsClosedOnObjectFailureCancellationAndInvalidConfiguration(t *testing.T) {
	for _, socket := range []string{"tcp://scanner:3310", "relative.sock", "/run/../clam.sock", "/run/clam\n.sock"} {
		if _, err := NewClamAVScanner(clamObjects{}, socket, time.Second); err == nil {
			t.Fatal("non-private scanner configuration accepted")
		}
	}
	if _, err := NewClamAVScanner(nil, "/run/clam.sock", time.Second); err == nil {
		t.Fatal("missing object reader accepted")
	}
	scanner, _ := NewClamAVScanner(clamObjects{fail: true}, "/run/nonexistent.sock", time.Second)
	if _, err := scanner.Scan(context.Background(), clamTestKey); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	scanner, _ = NewClamAVScanner(clamObjects{body: []byte("document")}, "/run/nonexistent.sock", time.Second)
	if _, err := scanner.Scan(context.Background(), "unscoped"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanner.Scan(ctx, clamTestKey); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	if err := scanner.Ready(ctx); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
}

func TestClamAVNeverPublishesEmptyOrOversizedStreams(t *testing.T) {
	for _, size := range []int{0, 15<<20 + 1} {
		socket := clamScanSocketTest(t, func(connection net.Conn) { _, _ = io.Copy(io.Discard, connection) })
		scanner, _ := NewClamAVScanner(clamObjects{body: make([]byte, size)}, socket, time.Second)
		if result, err := scanner.Scan(context.Background(), clamTestKey); !errors.Is(err, ErrDependency) || result.Clean {
			t.Fatalf("invalid stream became clean: %+v %v", result, err)
		}
	}
}

func TestClamAVRejectsStaleFutureOrMalformedLoadedDefinitions(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for name, version := range map[string]string{
		"stale":        "1.4.6/28163/" + now.Add(-48*time.Hour-time.Second).Format("Mon Jan _2 15:04:05 2006"),
		"future":       "1.4.6/28163/" + now.Add(5*time.Minute+time.Second).Format("Mon Jan _2 15:04:05 2006"),
		"missing date": "1.4.6/28163", "invalid date": "1.4.6/28163/date", "zero database": "1.4.6/0/" + now.Format("Mon Jan _2 15:04:05 2006"), "invalid engine": "error/28163/" + now.Format("Mon Jan _2 15:04:05 2006"),
	} {
		t.Run(name, func(t *testing.T) {
			socket := clamSocketTest(t, func(connection net.Conn) {
				_, _ = bufio.NewReader(connection).ReadString(0)
				_, _ = io.WriteString(connection, "ClamAV "+version+" | COMMANDS: INSTREAM\x00")
			})
			scanner, _ := NewClamAVScannerWithFreshness(clamObjects{body: []byte("private")}, socket, time.Second, 48*time.Hour, func() time.Time { return now })
			if result, err := scanner.Scan(context.Background(), clamTestKey); !errors.Is(err, ErrDependency) || result.Clean {
				t.Fatalf("untrusted definitions published bytes: %+v %v", result, err)
			}
		})
	}
	for _, age := range []time.Duration{0, 59 * time.Minute, 8 * 24 * time.Hour} {
		if _, err := NewClamAVScannerWithFreshness(clamObjects{}, "/run/clam.sock", time.Second, age, time.Now); err == nil {
			t.Fatal("unsafe age accepted")
		}
	}
	if _, err := NewClamAVScannerWithFreshness(clamObjects{}, "/run/clam.sock", time.Second, 48*time.Hour, nil); err == nil {
		t.Fatal("missing clock accepted")
	}
}
