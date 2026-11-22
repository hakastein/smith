package broker_test

import (
	"context"
	"testing"

	"github.com/hakastein/smith/broker"
)

// Команда вне allow не доходит до обработчика, даже если её argv начинается разрешённым словом.
func TestListenRefusesCallsOutsideAllow(t *testing.T) {
	var handled [][]string
	s, err := broker.Listen([]string{"notes post", "notes list"}, func(_ context.Context, c broker.Call) broker.Reply {
		handled = append(handled, c.Argv)
		return broker.Reply{Stdout: "ok\n"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct {
		argv []string
		exit int
	}{
		{[]string{"notes", "post", "--text", "x"}, 0},
		{[]string{"notes", "delete"}, 2},
		{[]string{"notes"}, 2},
		{[]string{"notes-post"}, 2},
	} {
		r, err := broker.Do(context.Background(), s.Path, broker.Call{Argv: c.argv, Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if r.Exit != c.exit {
			t.Errorf("%v: код %d, ждали %d", c.argv, r.Exit, c.exit)
		}
	}
	if len(handled) != 1 {
		t.Fatalf("обработчик получил %v", handled)
	}
}
