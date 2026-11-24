package codex_test

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
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

// Расход запуска складывается из ходов. Из input_tokens вычитаются токены, прочитанные из кэша и записанные в него;
// записанные стоят по цене записи в кэш.
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

// Расход субагентов — из их журналов: субагент найден по записи о запуске в журнале агента, его запросы стоят по
// модели его журнала. Контекст этапа — контекст самого агента; журнал чужой сессии в расход не входит.
func TestSubagentUsage(t *testing.T) {
	small := `{"input_tokens":1000,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0}`
	large := `{"input_tokens":5000,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0}`
	main := `{"type":"token_usage_record","payload":{"thread_id":"T","usage":` + small + `}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","agent_thread_id":"C"}}}`
	child := `{"type":"turn_context","payload":{"model":"gpt-6.1-terra"}}` + "\n" +
		`{"type":"token_usage_record","payload":{"thread_id":"C","usage":` + large + `}}`
	other := `{"type":"token_usage_record","payload":{"thread_id":"X","usage":` + large + `}}`
	out, err := start(t, t.TempDir(), drivers.Args{Model: "gpt-6.2-sol", Subagents: true}, `mkdir -p "$CODEX_HOME/sessions"
printf '%s\n' '`+main+`' > "$CODEX_HOME/sessions/rollout-1-T.jsonl"
printf '%s\n' '`+child+`' > "$CODEX_HOME/sessions/rollout-2-C.jsonl"
printf '%s\n' '`+other+`' > "$CODEX_HOME/sessions/rollout-3-X.jsonl"
echo '{"type":"thread.started","thread_id":"T"}'
echo '{"type":"turn.completed","usage":`+small+`}'
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]agent.Usage{"gpt-6.2-sol": {InputTokens: 1000, OutputTokens: 10}, "gpt-6.1-terra": {InputTokens: 5000, OutputTokens: 10}}
	if !maps.Equal(out.Usage, want) {
		t.Errorf("usage %+v, want %+v", out.Usage, want)
	}
	if out.Cost == nil || math.Abs(*out.Cost-(1000*2+10*10+5000*1+10*5)/1e6) > 1e-12 {
		t.Errorf("cost %v", out.Cost)
	}
	if out.ContextTokens != 1000 {
		t.Errorf("context %d, want 1000", out.ContextTokens)
	}
}

// Субагенты выключены, пока запуск их не разрешил, в любой песочнице.
func TestSubagentsOffByDefault(t *testing.T) {
	for _, sandbox := range []drivers.Sandbox{{}, {ReadOnlyCLI: true}} {
		for _, on := range []bool{false, true} {
			argv, err := run(t, drivers.Args{Sandbox: sandbox, Subagents: on})
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("agents.enabled=%t", on); !slices.Contains(argv, want) {
				t.Errorf("%+v, Subagents=%t: нет %s в argv %q", sandbox, on, want, argv)
			}
		}
	}
}

// Лимиты подписки — из самой поздней записи, даже когда она в журнале агента, а журнал субагента читается после.
func TestLatestLimitsAcrossSubagents(t *testing.T) {
	limits := func(at, reached string) string {
		return `{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"token_count","rate_limits":{"rate_limit_reached_type":"` + reached + `"}}}`
	}
	main := `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","agent_thread_id":"C"}}}` + "\n" +
		limits("2026-11-24T20:07:34.000Z", "primary")
	out, err := start(t, t.TempDir(), drivers.Args{Subagents: true}, `mkdir -p "$CODEX_HOME/sessions"
printf '%s\n' '`+main+`' > "$CODEX_HOME/sessions/rollout-1-T.jsonl"
printf '%s\n' '`+limits("2026-11-24T20:07:30.000Z", "")+`' > "$CODEX_HOME/sessions/rollout-2-C.jsonl"
echo '{"type":"thread.started","thread_id":"T"}'
`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits == nil || out.Limits.Status == nil || *out.Limits.Status != "rejected" {
		t.Errorf("limits %+v, want rejected", out.Limits)
	}
}
