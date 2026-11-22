package agent_test

import (
	"testing"

	"github.com/hakastein/smith/agent"
)

type discard struct{}

func (discard) Write(*agent.Event) {}
func (discard) Raw([]byte)         {}
func (discard) End()               {}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Ответ модели открывает волну, а этап, так и не дошедший до ответа, не вешает ждущего.
func TestRespondingOnStartOrEnd(t *testing.T) {
	started := agent.NewTelemetry("started", nil, nil, t.TempDir(), discard{})
	started.Accept(&agent.Event{Kind: "system", Note: "init"})
	if closed(started.Responding()) {
		t.Fatal("Responding закрыт до ответа модели")
	}
	started.Accept(&agent.Event{Kind: "stream_event", Start: true})
	if !closed(started.Responding()) {
		t.Fatal("Responding не закрыт началом ответа")
	}
	started.Close()

	silent := agent.NewTelemetry("silent", nil, nil, t.TempDir(), discard{})
	silent.Close()
	if !closed(silent.Responding()) {
		t.Fatal("Responding не закрыт концом этапа без ответа")
	}
}
