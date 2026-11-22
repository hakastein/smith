package codex_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/drivers"
	"github.com/hakastein/smith/drivers/codex"
)

type discard struct{}

func (discard) Write(*agent.Event) {}
func (discard) Raw([]byte)         {}
func (discard) End()               {}

var quiet = slog.New(slog.DiscardHandler)

// run запускает драйвер с a на подменённом codex и возвращает argv, с которым тот был вызван.
func run(t *testing.T, a drivers.Args) ([]string, error) {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > '"+argvFile+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := codex.New(drivers.Settings{Bin: bin}, drivers.Setup{Env: map[string]string{"HOME": dir, "PATH": "/usr/bin:/bin"},
		EnvOrder: []string{"HOME", "PATH"}, StageBin: dir, Tmp: dir, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	a.Name, a.Model, a.Effort, a.Cwd = "stage", "model", "low", dir
	telemetry := agent.NewTelemetry(a.Name, nil, nil, dir, discard{}, quiet)
	defer telemetry.Close()
	if _, err := d.Run(context.Background(), a, telemetry); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"), nil
}

// Задание без песочницы запускает codex в строгом профиле прав, а не в его режиме только для чтения.
func TestZeroSandboxIsStrict(t *testing.T) {
	argv, err := run(t, drivers.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, `default_permissions="smith"`) || slices.Contains(argv, "--sandbox") {
		t.Fatalf("codex запущен не в строгой песочнице: %q", argv)
	}
}

// Системный промпт строгого этапа доходит до codex, а отсутствующий файл срывает запуск в любой песочнице.
func TestStrictSystemPromptFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shared.md")
	if err := os.WriteFile(file, []byte("общие материалы"), 0o666); err != nil {
		t.Fatal(err)
	}
	argv, err := run(t, drivers.Args{SystemPromptFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, `developer_instructions="общие материалы"`) {
		t.Errorf("системного промпта нет в argv: %q", argv)
	}
	for _, sandbox := range []drivers.Sandbox{{}, {ReadOnlyCLI: true}} {
		if _, err := run(t, drivers.Args{SystemPromptFile: file + ".missing", Sandbox: sandbox}); err == nil {
			t.Errorf("%+v: запуск с отсутствующим файлом системного промпта не сорван", sandbox)
		}
	}
}
