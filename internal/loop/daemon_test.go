package loop

import (
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/sockapi"
	protocol "github.com/gsd-build/protocol-go"
)

func TestRelayURLIncludesMachineIDOnly(t *testing.T) {
	cfg := &config.Config{
		MachineID: "machine-uuid-123",
		AuthToken: "token-with-special/chars+",
		RelayURL:  "wss://relay.example.com/ws/daemon",
	}

	got := buildRelayURL(cfg)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("machineId") != "machine-uuid-123" {
		t.Errorf("missing or wrong machineId: %q", q.Get("machineId"))
	}
	if q.Get("token") != "" {
		t.Errorf("token must NOT be in URL (leaked to logs); got: %q", q.Get("token"))
	}
	if parsed.Host != "relay.example.com" {
		t.Errorf("unexpected host: %q", parsed.Host)
	}
	if parsed.Path != "/ws/daemon" {
		t.Errorf("unexpected path: %q", parsed.Path)
	}
}

func TestRelayURLPreservesExistingQuery(t *testing.T) {
	cfg := &config.Config{
		MachineID: "machine-uuid-123",
		AuthToken: "token-with-special/chars+",
		RelayURL:  "wss://relay.example.com/ws/daemon?version=2",
	}

	got := buildRelayURL(cfg)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("machineId") != "machine-uuid-123" {
		t.Errorf("missing or wrong machineId: %q", q.Get("machineId"))
	}
	if q.Get("token") != "" {
		t.Errorf("token must NOT be in URL; got: %q", q.Get("token"))
	}
	if q.Get("version") != "2" {
		t.Errorf("existing query param lost; version: %q", q.Get("version"))
	}
	if parsed.Host != "relay.example.com" {
		t.Errorf("unexpected host: %q", parsed.Host)
	}
	if parsed.Path != "/ws/daemon" {
		t.Errorf("unexpected path: %q", parsed.Path)
	}
}

func TestGracefulShutdownCallsStopAll(t *testing.T) {
	stopped := false
	mgr := &mockManager{stopAllFn: func() { stopped = true }}

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: mgr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate shutdown

	d.gracefulShutdown(ctx)

	if !stopped {
		t.Error("expected StopAll to be called during graceful shutdown")
	}
}

func TestStatusUsesRelayConnectionState(t *testing.T) {
	d := &Daemon{
		cfg:       &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:   "test",
		manager:   &mockManager{},
		client:    relayClientStub(true),
		startedAt: time.Now().Add(-5 * time.Second),
	}

	if !d.Status().RelayConnected {
		t.Fatal("expected status to report connected relay")
	}

	d.client = relayClientStub(false)
	if d.Status().RelayConnected {
		t.Fatal("expected status to report disconnected relay")
	}
}

func TestStatusUsesEffectiveConfiguredConcurrency(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			MachineID:          "m1",
			RelayURL:           "wss://localhost/ws",
			MaxConcurrentTasks: 2,
		},
		version:   "test",
		manager:   &mockManager{},
		client:    relayClientStub(true),
		startedAt: time.Now().Add(-5 * time.Second),
	}

	if got := d.Status().MaxConcurrentTasks; got != 2 {
		t.Fatalf("expected configured max concurrency 2, got %d", got)
	}
}

// mockManager implements SessionManager for testing.
type mockManager struct {
	stopAllFn func()
	getFn     func(sessionID string) *session.Actor
}

func (m *mockManager) Get(sessionID string) *session.Actor {
	if m.getFn != nil {
		return m.getFn(sessionID)
	}
	return nil
}
func (m *mockManager) Spawn(ctx context.Context, opts session.Options) (*session.Actor, error) {
	return nil, nil
}
func (m *mockManager) ActiveTaskIDs() []string                                                    { return nil }
func (m *mockManager) ActiveCount() (total int, executing int)                                    { return 0, 0 }
func (m *mockManager) InFlightCount() int                                                         { return 0 }
func (m *mockManager) StartReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration) {}
func (m *mockManager) StopAll() {
	if m.stopAllFn != nil {
		m.stopAllFn()
	}
}
func (m *mockManager) SessionInfos() []sockapi.SessionInfo { return nil }

func relayClientStub(connected bool) *relay.Client {
	c := relay.NewClient(relay.Config{
		URL:           "wss://relay.example.com/ws/daemon",
		AuthToken:     "token",
		MachineID:     "m1",
		DaemonVersion: "test",
		OS:            "darwin",
		Arch:          "arm64",
	})
	c.SetConnectedForTest(connected)
	return c
}

type loopFakeRelay struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []any
}

func newLoopFakeRelay() *loopFakeRelay {
	r := &loopFakeRelay{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *loopFakeRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}

func (r *loopFakeRelay) countTaskCompletes(taskID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, frame := range r.frames {
		if tc, ok := frame.(*protocol.TaskComplete); ok && tc.TaskID == taskID {
			count++
		}
	}
	return count
}

func (r *loopFakeRelay) waitForTaskStarted(t *testing.T, timeout time.Duration, taskID string) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		for _, frame := range r.frames {
			if ts, ok := frame.(*protocol.TaskStarted); ok && ts.TaskID == taskID {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		r.cond.Wait()
	}
}

func buildFakeClaudeBinary(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/fake-claude")
	cmd.Dir = daemonDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-claude: %v\n%s", err, out)
	}
	return binPath
}

func TestHandleTaskIgnoresDuplicateTaskID(t *testing.T) {
	binPath := buildFakeClaudeBinary(t)
	t.Setenv("FAKE_CLAUDE_SLEEP", "1")

	relaySink := newLoopFakeRelay()
	actor, err := session.NewActor(session.Options{
		SessionID:  "sess-dup",
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Relay:      relaySink,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: &mockManager{
			getFn: func(sessionID string) *session.Actor {
				if sessionID == "sess-dup" {
					return actor
				}
				return nil
			},
		},
		client: relayClientStub(false),
	}

	msg := &protocol.Task{
		TaskID:    "dup-task-1",
		SessionID: "sess-dup",
		ChannelID: "ch-dup",
		Prompt:    "hello",
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("first handleTask: %v", err)
	}
	if !relaySink.waitForTaskStarted(t, 2*time.Second, "dup-task-1") {
		t.Fatal("expected task to start")
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("duplicate handleTask: %v", err)
	}

	time.Sleep(3500 * time.Millisecond)

	if got := relaySink.countTaskCompletes("dup-task-1"); got != 1 {
		t.Fatalf("expected duplicate task to be ignored, got %d TaskComplete frames", got)
	}
}
