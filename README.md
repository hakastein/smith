# smith

Go-библиотека для запуска headless-агентов — Claude Code и Codex — над задачей из вашего сервиса.

smith запускает CLI агента в песочнице, выдаёт ему ровно те скиллы и команды, которые нужны для задачи, и возвращает
результат в одной форме для обоих CLI: ответ, события, токены, размер контекста и цену.

## Возможности

- **Изолированные скиллы и инструменты.** Каждый запуск видит только свои скиллы и свои команды. Личные настройки,
  хуки, плагины и скиллы пользователя машины в запуск не попадают.
- **Команды агента на Go.** Команда, которую вызывает агент, исполняется обработчиком в вашем сервисе: агент получает
  её вывод, а секреты, сеть и доступ к API остаются на стороне сервиса.
- **Песочница по умолчанию.** Агент читает только рабочий каталог и явно названные пути, сети у него нет.
- **Один интерфейс для Claude Code и Codex.** Модель, усилие, задание, JSON Schema ответа и таймаут задаются одинаково;
  драйвер переводит их во флаги своего CLI.
- **Учёт расхода.** Токены по моделям, размер контекста, цена запуска и лимиты подписки.
- **Управляемый жизненный цикл.** Отмена контекста или таймаут завершают всю группу процессов агента; события
  запуска идут в ваш `*slog.Logger` и в ваш поток.
- **Параллельные запуски на общем кэше промпта.** Можно дождаться, когда модель начала отвечать первому запуску, и
  стартовать остальные на прогретом кэше.

## Установка

```bash
go get github.com/hakastein/smith
```

Нужны Linux или macOS, Go 1.26 и `claude` или `codex` с выполненным входом. Песочнице Claude Code в Linux нужны
`bubblewrap` и `socat`.

## Быстрый старт

```go
import (
	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/drivers"
	_ "github.com/hakastein/smith/drivers/codex"
)

codex := drivers.Get("codex")
d, err := codex.New(codex.Defaults, drivers.Setup{
	Env:      map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")},
	EnvOrder: []string{"PATH", "HOME"},
	Tmp:      tmp,    // здесь драйвер готовит каталоги запусков
	Logger:   logger, // nil — slog.Default()
})

t := agent.NewTelemetry("summary", nil, nil, dir, stream, logger)
out, err := d.Run(ctx, drivers.Args{
	Name:    "summary",
	Model:   "gpt-6.1-terra",
	Effort:  "low",
	Prompt:  "Опиши изменения в этом репозитории для пользователей.",
	Cwd:     repo,
	Schema:  "answer.schema.json",
	Timeout: 10 * time.Minute,
}, t)
if err != nil {
	t.Close()
	return err
}
t.Finish(out)
if !out.OK {
	return fmt.Errorf("агент не справился: %s", out.Reason) // auth, rate_limit, timeout, died, killed:<сигнал>, other
}
answer := out.Result.Text
```

`drivers.Gaps` до запуска сообщает, чего не хватает окружению: бинарника CLI или входа.

`Outcome` содержит `OK` и `Reason`, ответ `Result`, события `Events`, расход `Usage` по моделям, `ContextTokens`,
цену `Cost` (`nil`, если цена модели неизвестна) и команду `Resume` для продолжения сессии, если задан `SessionDir`.

## Скиллы

Скиллы собираются в библиотеку из встроенной файловой системы и каталогов на диске, и каждый запуск получает только
перечисленные в `Args.Skills`:

```go
lib, err := skills.LoadLibrary(embedded, []string{"./skills"})
runSkills, err := lib.Materialize(tmp, []string{"changelog", "glossary"})
// drivers.Setup{RunSkills: runSkills, …}
out, err := d.Run(ctx, drivers.Args{ /* … */ Skills: []string{"changelog"}}, t)
```

## Команды агента

Команда агента — обработчик в вашем сервисе. Агент вызывает её как обычную программу; в песочнице у него только
тонкий клиент, который передаёт argv и рабочий каталог по Unix-сокету запуска и возвращает stdout, stderr и код
выхода.

```go
sock, err := broker.Listen([]string{"notes post"}, func(ctx context.Context, c broker.Call) broker.Reply {
	// c.Argv — ["notes", "post", ...], c.Cwd — каталог вызова
	return broker.Reply{Stdout: "записано\n"}
})
defer sock.Close()

out, err := d.Run(ctx, drivers.Args{ /* … */
	Allow:   []string{"notes post"},
	Socket:  sock.Path,
	Sandbox: drivers.Sandbox{Readable: []string{bin, filepath.Dir(exe)}},
}, t)
```

Клиент — бинарь вашего сервиса под именем команды в каталоге `Setup.StageBin` (ссылка или обёртка), который вызывает
`broker.Main(os.Args[0], os.Args[1:], os.Stdout, os.Stderr)`.

`Allow` — префиксы команд из слов, например `git log` или `notes post`. Драйвер разрешает их правилами своего CLI;
вызов команды брокера вне `Allow` отклоняется до обработчика. Сокет запуска лежит в собственном каталоге, и песочница
открывает его только этому запуску. `Close` даёт вызовам в работе 5 секунд, затем отменяет их контекст.

## Песочница

Нулевое значение `Args.Sandbox` — строгая песочница: агент читает рабочий каталог, `Sandbox.Readable`,
`Settings.Readable` драйвера, свои скиллы и каталог своего сокета; сети нет.

| | Claude Code | Codex |
|---|---|---|
| Файлы | Инструменты видят рабочий каталог и `--add-dir`; команды идут в песочнице ОС, где открыты только пути запуска и системные каталоги | Профиль прав `permissions.smith`: открыты минимальный набор системных путей, пути запуска и `HOME` запуска. Каталог установки codex нужно добавить в `Settings.Readable` |
| Сеть | Закрыта, команды вне песочницы запрещены | Закрыта прокси без разрешённых доменов, `web_search` выключен |
| Настройки | Свой `CLAUDE_CONFIG_DIR` с копией входа | Общий `CODEX_HOME` ради обновления токена; его `config.toml`, правила и скиллы запуск не читает. Личный `AGENTS.md` пока читается |

`Sandbox{ReadOnlyCLI: true}` ослабляет песочницу до режима CLI «только чтение»: агент читает любой путь, а команды из
`Allow` выполняются по правилам разрешений CLI, с сетью.

Окружение запуска собирается по белому списку (`PATH`, `HOME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`, `USER`, `SHELL`,
`TERM`, прокси, сертификаты, переменные входа CLI) плюс `Settings.Env` и `Args.Env`. Переменные вида `*_TOKEN`,
`*_KEY`, `*_SECRET`, `*_PASSWORD` вымарываются из сохранённых сессий; для своего лога используйте `redact.Secrets`.

## Параллельные запуски

Кэш промпта, записанный одним запросом, становится доступен другим, когда модель начинает отвечать.
`Telemetry.Responding()` закрывается в этот момент или по завершении запуска, если до ответа дело не дошло:

```go
g, ctx := errgroup.WithContext(ctx)
first := agent.NewTelemetry(tasks[0].Name, nil, nil, dir, streams[0], logger)
g.Go(func() error { return run(ctx, tasks[0], first) })
<-first.Responding()
for i, task := range tasks[1:] {
	g.Go(func() error { return run(ctx, task, agent.NewTelemetry(task.Name, nil, nil, dir, streams[i+1], logger)) })
}
err := g.Wait()
```

Общий префикс дают одинаковые `SystemPromptFile` и начало `Prompt`.

## Разработка

```bash
make go   # gofmt, go vet, тесты
```

Устройство пакетов — [ADR 1](docs/adr/0001-packages-and-api.md).

## Лицензия

[MIT](LICENSE)
