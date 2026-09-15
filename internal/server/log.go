package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// LogStreamer defines the interface for opening a live follow log stream for a container.
type LogStreamer interface {
	StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
}

// LogService implements supervisorv1connect.LogServiceHandler.
type LogService struct {
	supervisorv1connect.UnimplementedLogServiceHandler
	dataDir     string
	logStreamer LogStreamer
	bootLog     BootLogFile
}

// NewLogService constructs a LogService instance. bootLog (optional) reports
// the boot file the supervisor is currently appending to (docs/29 §5.1);
// without it, live-following boot logs is refused and the newest file is
// treated as current on a best-effort basis in listings.
func NewLogService(dataDir string, logStreamer LogStreamer, bootLog BootLogFile) *LogService {
	return &LogService{
		dataDir:     dataDir,
		logStreamer: logStreamer,
		bootLog:     bootLog,
	}
}

// runnerLogPath returns the expected location for a runner's compressed JSONL log file:
// DATA_DIR/logs/<runner-id>.log.jsonl.gz (OQ #14, #20).
func runnerLogPath(dataDir, runnerID string) string {
	return filepath.Join(dataDir, "logs", fmt.Sprintf("%s.log.jsonl.gz", runnerID))
}

// StreamRunnerLogs opens a live follow tail on a runner container and streams LogChunks.
func (s *LogService) StreamRunnerLogs(ctx context.Context, req *connect.Request[supervisorv1.StreamRunnerLogsRequest], stream *connect.ServerStream[supervisorv1.LogChunk]) error {
	runnerID := strings.TrimSpace(req.Msg.RunnerId)
	if runnerID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("runner_id must not be empty"))
	}

	if s.logStreamer == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("live log streaming is not available"))
	}

	rawStream, err := s.logStreamer.StreamLogs(ctx, runnerID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("opening log stream for runner %q: %w", runnerID, err))
	}
	defer func() { _ = rawStream.Close() }()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = rawStream.Close()
		case <-done:
		}
	}()

	reader := bufio.NewReader(rawStream)

	for {
		select {
		case <-ctx.Done():
			return nil // clean stream teardown on client cancel
		default:
		}

		// Peek at 8-byte header to distinguish Docker multiplexed framing from plain text
		peek, err := reader.Peek(8)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) || ctx.Err() != nil {
				return nil
			}
			// If fewer than 8 bytes available, read remaining lines plainly
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				if err := sendPlainLine(scanner.Text(), stream); err != nil {
					return err
				}
			}
			return nil
		}

		// Docker multiplexed header: byte 0 is 1 (stdout) or 2 (stderr), bytes 1-3 are zero
		if peek[1] == 0 && peek[2] == 0 && peek[3] == 0 && (peek[0] == 1 || peek[0] == 2) {
			header := make([]byte, 8)
			if _, err := io.ReadFull(reader, header); err != nil {
				return nil
			}

			streamType := "stdout"
			if header[0] == 2 {
				streamType = "stderr"
			}
			size := binary.BigEndian.Uint32(header[4:8])
			payload := make([]byte, size)
			if _, err := io.ReadFull(reader, payload); err != nil {
				return nil
			}

			timestamp, content := parseDockerTimestamp(string(payload))
			if err := stream.Send(&supervisorv1.LogChunk{
				Timestamp: timestamp,
				Stream:    streamType,
				Content:   content,
			}); err != nil {
				return err
			}
		} else {
			// Plain text stream fallback
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				if err := sendPlainLine(line, stream); err != nil {
					return err
				}
			}
			if err != nil {
				return nil
			}
		}
	}
}

func sendPlainLine(raw string, stream *connect.ServerStream[supervisorv1.LogChunk]) error {
	trimmed := strings.TrimRight(raw, "\r\n")
	if trimmed == "" {
		return nil
	}
	ts, content := parseDockerTimestamp(trimmed)
	return stream.Send(&supervisorv1.LogChunk{
		Timestamp: ts,
		Stream:    "stdout",
		Content:   content,
	})
}

func parseDockerTimestamp(raw string) (string, string) {
	content := raw
	timestamp := ""
	if idx := strings.IndexByte(content, ' '); idx > 0 {
		cand := content[:idx]
		if _, err := time.Parse(time.RFC3339Nano, cand); err == nil {
			timestamp = cand
			content = content[idx+1:]
		} else if _, err := time.Parse(time.RFC3339, cand); err == nil {
			timestamp = cand
			content = content[idx+1:]
		}
	}
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	return timestamp, strings.TrimRight(content, "\r\n")
}

// GetRunnerLogs reads stored gzipped JSONL logs from DATA_DIR/logs/ for a completed runner.
// Reads are bounded: the runner id is path-validated (docs/29 §5.2) and only the
// last tail_lines entries are decoded (default 500, cap 5000) so captures at the
// retention size cap cannot become unbounded responses (docs/29 §5.1).
func (s *LogService) GetRunnerLogs(ctx context.Context, req *connect.Request[supervisorv1.GetRunnerLogsRequest]) (*connect.Response[supervisorv1.GetRunnerLogsResponse], error) {
	runnerID, err := safeLogResourceName(req.Msg.RunnerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if req.Msg.TailLines < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tail_lines must not be negative"))
	}

	tail := int(req.Msg.TailLines)
	if tail == 0 {
		tail = bootFileReplayDefault
	} else if tail > bootFileReplayMax {
		tail = bootFileReplayMax
	}

	srcPath := runnerLogPath(s.dataDir, runnerID)
	f, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no logs found for runner %q", runnerID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("opening log file: %w", err))
	}
	defer func() { _ = f.Close() }()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reading compressed log stream: %w", err))
	}
	defer func() { _ = gzReader.Close() }()

	// Ring buffer: decode the whole capture (required — gzip stream) but
	// retain only the last `tail` entries in the response.
	chunks := make([]*supervisorv1.LogChunk, 0, min(tail, 64))
	scanner := bufio.NewScanner(gzReader)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry struct {
			Timestamp string `json:"timestamp"`
			Stream    string `json:"stream"`
			Content   string `json:"content"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parsing log entry: %w", err))
		}
		chunks = append(chunks, &supervisorv1.LogChunk{
			Timestamp: entry.Timestamp,
			Stream:    entry.Stream,
			Content:   entry.Content,
		})
		if len(chunks) > tail {
			chunks = chunks[1:]
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scanning log lines: %w", err))
	}

	return connect.NewResponse(&supervisorv1.GetRunnerLogsResponse{
		Lines: chunks,
	}), nil
}
