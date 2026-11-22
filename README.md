# smith

Go-фреймворк запуска headless-агентов. smith запускает один этап агента — Claude Code или Codex — в песочнице и
отдаёт его исход в общей для драйверов форме: ответ, события потока, токены, размер контекста и цену. Порядок этапов,
параллельность и предметная логика — обычный Go-код сервиса: ни конфигов этапов, ни движка сценариев в smith нет.

Раскладка пакетов и решения — [ADR 1](docs/adr/0001-packages-and-api.md).

## Требования

- Linux или macOS, Go 1.26.
- `claude` или `codex` с выполненным входом.
- Строгая песочница `claude` в Linux — `bubblewrap` и `socat`: без них этап не стартует.

## Этап

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
	StageBin: bin, // каталог команд агента: первым в PATH этапа
	Tmp:      tmp, // временный каталог прогона: в нём драйвер готовит каталоги этапов
	Logger:   logger, // *slog.Logger сервиса; nil — slog.Default()
})

t := agent.NewTelemetry("writer", nil, nil, dir, stream, logger)
out, err := d.Run(ctx, drivers.Args{Name: "writer", Model: "gpt-6.1-terra", Effort: "low", Prompt: task, Cwd: dir,
	Schema: "answer.schema.json", Sandbox: drivers.Sandbox{Readable: []string{repo}}, Timeout: 10 * time.Minute}, t)
if err != nil {
	t.Close()
	return err
}
t.Finish(out)
if !out.OK {
	return fmt.Errorf("этап сорван: %s", out.Reason) // auth, rate_limit, timeout, died, killed:<сигнал>, other
}
answer := out.Result.Text
```

`drivers.Args` — задание этапа: модель и усилие, задание (`Prompt`, в stdin CLI), рабочий каталог, скиллы, файл
системного промпта (`SystemPromptFile`: у этапов с одним файлом общий префикс кэша), JSON Schema ответа, переменные
сверх белого списка, таймаут, разрешённые команды и сокет команд. `drivers.Gaps` до запуска называет, чего не хватает
окружению: бинарника или входа.

`agent.Telemetry` пишет события этапа в `agent.Stream` сервиса (`Write` — событие в общей форме, `Raw` — строка
вывода CLI как есть), а ход этапа, его события и итог — записями в `*slog.Logger` сервиса. Контекст — атрибутами:
`stage` у записей этапа, `driver` у записей драйвера; строка события агента — запись с атрибутом `event` (вид строки:
`session`, `note`, `call`, `text`, `thinking`, `reply` с `error`, `stderr`, `unparsed`, `failed`). `Outcome` — исход:
`OK` и `Reason`, итог `Result`, события `Events`, `Usage` по моделям, `ContextTokens`, `Cost` (`nil` — цена
неизвестна), лимиты подписки, команда продолжения сессии `Resume`, если задан `SessionDir`.

Отмена `ctx` или таймаут этапа убивают всю группу процессов агента. Секреты окружения сервиса (`*_TOKEN`, `*_KEY`,
`*_SECRET`, `*_PASSWORD`) вымарываются из сохранённых сессий. В записях лога — текст модели и argv агента: обработчик
лога сервиса вымарывает их `redact.Secrets`, как и то, что сервис пишет сам.

## Волна на прогретом кэше

Кэш промпта, записанный запросом, доступен соседним запросам с началом ответа модели. Этапы волны с общим префиксом
задания стартуют после того, как первому начали отвечать: `Telemetry.Responding()` закрывается началом ответа или
концом этапа, если до ответа он не дошёл.

```go
g, ctx := errgroup.WithContext(ctx)
lead := agent.NewTelemetry(stages[0].Name, nil, nil, dir, streams[0], logger)
g.Go(func() error { return run(ctx, stages[0], lead) })
<-lead.Responding()
for i, s := range stages[1:] {
	g.Go(func() error { return run(ctx, s, agent.NewTelemetry(s.Name, nil, nil, dir, streams[i+1], logger)) })
}
err := g.Wait()
```

`run` — `d.Run` и `Finish`/`Close` телеметрии, как выше.

## Команды агента

Команда агента — Go-обработчик сервиса. В песочнице у агента только тонкий клиент: он передаёт argv и рабочий каталог
по Unix-сокету этапа, сервис исполняет вызов и возвращает stdout, stderr и код выхода.

```go
sock, err := broker.Listen([]string{"notes post"}, func(ctx context.Context, c broker.Call) broker.Reply {
	// c.Argv — ["notes", "post", ...], c.Cwd — каталог вызова
	return broker.Reply{Stdout: "записано\n"}
})
defer sock.Close() // вызовы в работе получают 5 секунд, потом их контекст отменяется, а Close возвращает ошибку
out, err := d.Run(ctx, drivers.Args{ /* … */ Allow: []string{"notes post"}, Socket: sock.Path,
	Sandbox: drivers.Sandbox{Readable: []string{bin, filepath.Dir(exe)}}}, t)
```

Клиент — бинарь сервиса под именем команды в `StageBin` (ссылка или обёртка), который зовёт
`broker.Main(os.Args[0], os.Args[1:], os.Stdout, os.Stderr)`; каталог `StageBin` и каталог бинаря открывают песочнице
на чтение. Сокет лежит в своём временном каталоге, и строгая песочница открывает этот каталог только своему этапу.
`allow` — префиксы из слов: драйвер разрешает их своими правилами, а вызов команды брокера вне них отклоняется кодом 2
и до обработчика не доходит.

## Песочница

`drivers.Args.Sandbox` — песочница этапа; нулевое значение — строгая: агент читает только рабочий каталог,
`Readable` этапа и `Settings.Readable` драйвера, свои скиллы и каталог сокета; сети нет, Unix-сокеты открыты.
Ослабление — явное: `Sandbox{ReadOnlyCLI: true}` запускает CLI в его режиме только для чтения, где агент читает любой
путь, а команды `allow` идут по правилам разрешений CLI вне его песочницы, с сетью.

| | `claude` | `codex` |
|---|---|---|
| Файлы | Инструменты читают рабочий каталог и `--add-dir`; команды — в песочнице ОС: чтение закрыто, кроме путей этапа и `/usr`, `/bin`, `/lib`, `/lib64`, `/etc` | Профиль прав `permissions.smith`: `:root` закрыт, открыты `:minimal`, пути этапа и `HOME` этапа. Каталог установки codex — в `Settings.Readable`, иначе команды агента не запускаются |
| Сеть | Без разрешённых доменов, команды вне песочницы запрещены | Прокси без разрешённых доменов, `web_search` выключен |
| Вход и настройки | Свой `CLAUDE_CONFIG_DIR` с копией кредов: личные настройки, хуки и плагины в этап не попадают | Общий `CODEX_HOME` с `auth.json`: копия разорвала бы обновление токена. Его `config.toml` и правила этап не читает |

Окружение этапа — белый список: `PATH`, `HOME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`, `USER`, `SHELL`, `TERM`, прокси,
сертификаты, переменные входа драйвера и `Settings.Env`.

## Разработка

```bash
make go      # gofmt, go vet, тесты
```

## Лицензия

[MIT](LICENSE)
