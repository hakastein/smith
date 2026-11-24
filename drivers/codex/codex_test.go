package codex_test

import (
	"context"
	"log/slog"
	"math"
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
	if _, err := start(t, dir, a, "printf '%s\\0' \"$@\" > '"+argvFile+"'\n"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"), nil
}

// start запускает драйвер с a на codex — скрипте оболочки script в каталоге dir, он же HOME и рабочий каталог.
func start(t *testing.T, dir string, a drivers.Args, script string) (*agent.Outcome, error) {
	t.Helper()
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := codex.New(drivers.Settings{Bin: bin}, drivers.Setup{Env: map[string]string{"HOME": dir, "PATH": "/usr/bin:/bin"},
		EnvOrder: []string{"HOME", "PATH"}, StageBin: dir, Tmp: dir, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	a.Name, a.Effort, a.Cwd = "stage", "low", dir
	if a.Model == "" {
		a.Model = "model"
	}
	telemetry := agent.NewTelemetry(a.Name, nil, nil, dir, discard{}, quiet)
	defer telemetry.Close()
	return d.Run(context.Background(), a, telemetry)
}

// Вход без кэша не включает прочитанные и записанные в кэш токены, рассуждение названо, расход — сумма ходов запуска,
// а запись в кэш оплачивается один раз — по своей цене.
func TestUsageAndCost(t *testing.T) {
	usage := `{"input_tokens":1000,"cached_input_tokens":600,"cache_write_input_tokens":300,"output_tokens":50,"reasoning_output_tokens":20}`
	record := `{"type":"token_usage_record","payload":{"usage":` + usage + `}}`
	out, err := start(t, t.TempDir(), drivers.Args{Model: "gpt-6.2-sol"}, `mkdir -p "$CODEX_HOME/sessions"
printf '%s\n%s\n' '`+record+`' '`+record+`' > "$CODEX_HOME/sessions/rollout-1-T.jsonl"
echo '{"type":"thread.started","thread_id":"T"}'
echo '{"type":"turn.completed","usage":`+usage+`}'
echo '{"type":"turn.completed","usage":`+usage+`}'
`)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.Usage{InputTokens: 200, OutputTokens: 100, CacheReadInputTokens: 1200, CacheCreationInputTokens: 600, ReasoningOutputTokens: 40}
	if got := out.Usage["gpt-6.2-sol"]; got != want {
		t.Errorf("usage %+v, want %+v", got, want)
	}
	if out.Cost == nil || math.Abs(*out.Cost-2*(100*2+600*0.1+300*2.5+50*10)/1e6) > 1e-12 {
		t.Errorf("cost %v", out.Cost)
	}
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
