package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hakastein/smith/agent"
)

type processStream struct {
	ready chan struct{}
	once  sync.Once
}

func (s *processStream) Write(e *agent.Event) {
	if e.Kind == "stage_stderr" && e.Stderr == "ready" {
		s.once.Do(func() { close(s.ready) })
	}
}
func (*processStream) Raw([]byte) {}
func (*processStream) End()       {}

// run — исход процесса и его строки stdout.
type run struct {
	exit  agent.Exit
	lines []string
}

func TestProcessCancellationIsLocalAndKillsDescendants(t *testing.T) {
	for _, byTimeout := range []bool{false, true} {
		name := "context"
		if byTimeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			start := func(ctx context.Context, script string, timeout time.Duration) (<-chan struct{}, <-chan run) {
				stream := &processStream{ready: make(chan struct{})}
				telemetry := agent.NewTelemetry(name, nil, nil, dir, stream, nil)
				result := make(chan run, 1)
				go func() {
					p := agent.Start(ctx, agent.Command{Path: "/bin/sh", Argv: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"},
						Dir: dir, Timeout: timeout}, telemetry)
					var r run
					for line := range p.Lines() {
						r.lines = append(r.lines, string(line))
					}
					r.exit = p.Wait()
					telemetry.Close()
					result <- r
				}()
				return stream.ready, result
			}
			awaitReady := func(ready <-chan struct{}) {
				t.Helper()
				select {
				case <-ready:
				case <-time.After(3 * time.Second):
					t.Fatal("процесс не запустился")
				}
			}
			awaitResult := func(done <-chan run) run {
				t.Helper()
				select {
				case r := <-done:
					return r
				case <-time.After(3 * time.Second):
					t.Fatal("дочерний процесс удерживает поток после отмены")
					return run{}
				}
			}
			otherCtx, stopOther := context.WithCancel(context.Background())
			defer stopOther()
			ready, other := start(otherCtx, `echo ready >&2; while [ ! -e release ]; do sleep 0.01; done; printf '%s\n' '{"result":"done"}'`, 0)
			awaitReady(ready)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := time.Duration(0)
			if byTimeout {
				timeout = 100 * time.Millisecond
			}
			// Потомок держит оба потока: убийство одной оболочки не даст процессу закончиться.
			ready, done := start(ctx, `sleep 30 & echo ready >&2; wait`, timeout)
			awaitReady(ready)
			if !byTimeout {
				cancel()
			}
			r := awaitResult(done)
			if !r.exit.Interrupted || r.exit.Signal != "SIGKILL" || r.exit.TimedOut != byTimeout {
				t.Fatalf("исход отменённого запуска: %+v", r.exit)
			}
			select {
			case r := <-other:
				t.Fatalf("отмена задела независимый запуск: %+v", r.exit)
			default:
			}
			if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			r = awaitResult(other)
			if r.exit.Interrupted || r.exit.Code == nil || *r.exit.Code != 0 || len(r.lines) != 1 || r.lines[0] != `{"result":"done"}` {
				t.Fatalf("независимый запуск не вернул исход и строки: %+v", r)
			}
		})
	}
}

// Wait возвращается сразу после выхода процесса, хотя фоновый потомок держит stderr, и убивает потомка.
func TestDescendantNeitherHoldsNorOutlivesTheStage(t *testing.T) {
	dir := t.TempDir()
	telemetry := agent.NewTelemetry("stage", nil, nil, dir, &processStream{ready: make(chan struct{})}, nil)
	defer telemetry.Close()
	started := time.Now()
	p := agent.Start(context.Background(), agent.Command{Path: "/bin/sh", Argv: []string{"-c", "sleep 30 >/dev/null & echo $! > pid; echo {}"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: dir}, telemetry)
	for range p.Lines() {
	}
	p.Wait()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("этап ждал потомка %s", elapsed)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("потомок пережил этап")
		}
	}
}
