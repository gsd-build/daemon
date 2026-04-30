package lab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type SessionStore struct {
	mu       sync.Mutex
	dir      string
	jsonl    string
	config   SessionConfig
	sequence int64
	redactor Redactor
}

func NewSessionStore(opts SessionStoreOptions) (*SessionStore, error) {
	if opts.RootDir == "" {
		return nil, fmt.Errorf("lab root dir is required")
	}
	id := time.Now().UTC().Format("20060102-150405.000000000") + "-session"
	dir := filepath.Join(opts.RootDir, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &SessionStore{
		dir:      dir,
		jsonl:    filepath.Join(dir, "session.jsonl"),
		config:   opts.Config,
		redactor: DefaultRedactor(),
	}, nil
}

func (s *SessionStore) Dir() string {
	return s.dir
}

func (s *SessionStore) Config() SessionConfig {
	return s.config
}

func (s *SessionStore) Append(kind string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	redactedPayload, records := s.redactPayload("", payload)
	event := SessionEvent{
		Sequence:  s.sequence,
		ID:        fmt.Sprintf("evt_%06d", s.sequence),
		Kind:      kind,
		Timestamp: nowRFC3339Nano(),
		Payload:   redactedPayload,
		Redacted:  records,
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	f, err := os.OpenFile(s.jsonl, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write session log: %w", err)
	}
	return nil
}

func (s *SessionStore) redactPayload(path string, value any) (any, []RedactEvent) {
	switch typed := value.(type) {
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return s.redactPayload(path, decoded)
		}
		return s.redactPayload(path, string(typed))
	case map[string]any:
		out := make(map[string]any, len(typed))
		var records []RedactEvent
		for key, val := range typed {
			childPath := joinRedactPath(path, key)
			redacted, childRecords := s.redactPayload(childPath, val)
			out[key] = redacted
			records = append(records, childRecords...)
		}
		return out, records
	case []any:
		out := make([]any, len(typed))
		var records []RedactEvent
		for index, val := range typed {
			childPath := joinRedactPath(path, strconv.Itoa(index))
			redacted, childRecords := s.redactPayload(childPath, val)
			out[index] = redacted
			records = append(records, childRecords...)
		}
		return out, records
	case string:
		return s.redactor.Redact(path, typed)
	default:
		return value, nil
	}
}

func joinRedactPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func (s *SessionStore) Export(opts ExportOptions) (ExportBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := readSessionEvents(s.jsonl)
	if err != nil {
		return ExportBundle{}, err
	}
	return ExportBundle{
		SchemaVersion: ExportSchemaVersion,
		ExportedAt:    nowRFC3339Nano(),
		Config:        s.config,
		Events:        events,
	}, nil
}

func readSessionEvents(path string) ([]SessionEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionEvent{}, nil
		}
		return nil, fmt.Errorf("open session log: %w", err)
	}
	defer f.Close()
	var events []SessionEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event SessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse session log: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan session log: %w", err)
	}
	return events, nil
}
