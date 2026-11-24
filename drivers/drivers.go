// Package drivers — общий интерфейс драйверов агента и их реестр. Драйвер
// запускает CLI агента в песочнице и отдаёт исход запуска в общей форме agent.
// Сами драйверы — в подпакетах, каждый регистрируется в init.
package drivers

import (
	"context"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/hakastein/smith/agent"
)

// Driver запускает CLI агента. События потока идут в телеметрию t, исход
// возвращается готовым; ошибка — запуск не подготовить, прогон дальше не идёт.
type Driver interface {
	Run(ctx context.Context, a Args, t *agent.Telemetry) (*agent.Outcome, error)
}

// Factory — то, что реестр знает о драйвере: настройки по умолчанию, проверка
// окружения и конструктор.
type Factory struct {
	Defaults Settings
	Gaps     func(ctx context.Context, s Settings, env map[string]string) []Gap
	New      func(s Settings, setup Setup) (Driver, error)
}

var registry = map[string]Factory{}

// Register добавляет драйвер под именем, которым его называет потребитель.
func Register(name string, f Factory) {
	if _, ok := registry[name]; ok {
		panic("драйвер " + name + " зарегистрирован дважды")
	}
	registry[name] = f
}

// Names — имена драйверов по алфавиту.
func Names() []string { return slices.Sorted(maps.Keys(registry)) }

// Get — драйвер по имени. Незарегистрированное имя — ошибка вызывающего: принятое имя он сверяет с Names.
func Get(name string) Factory {
	f, ok := registry[name]
	if !ok {
		panic("драйвер " + name + " не зарегистрирован")
	}
	return f
}

// Gaps — чего не хватает окружению для драйверов names.
func Gaps(ctx context.Context, names []string, settings map[string]Settings, env map[string]string) []Gap {
	var gaps []Gap
	for _, name := range names {
		gaps = append(gaps, Get(name).Gaps(ctx, settings[name], env)...)
	}
	return gaps
}

// New создаёт драйверы names, каждый со своими настройками.
func New(names []string, settings map[string]Settings, setup Setup) (map[string]Driver, error) {
	out := map[string]Driver{}
	for _, name := range names {
		d, err := Get(name).New(settings[name], setup)
		if err != nil {
			return nil, err
		}
		out[name] = d
	}
	return out, nil
}

// Gap — чего не хватает окружению для драйвера: бинарника или входа.
type Gap struct {
	ID      string `yaml:"id"`
	Message string `yaml:"message"`
}

// Setup — окружение прогона, общее для всех запусков драйвера.
type Setup struct {
	Env       map[string]string
	EnvOrder  []string // порядок переменных Env: в нём они идут в окружение запуска
	StageBin  string   // каталог команд, которые агенту можно вызывать: встаёт первым в PATH
	RunSkills string   // каталог скиллов прогона (skills.Library.Materialize) или пусто
	Tmp       string   // временный каталог прогона: в нём драйвер готовит каталоги запусков
	// Logger — лог драйвера; nil — slog.Default().
	Logger *slog.Logger
}

// Args — один запуск агента.
type Args struct {
	// Name — имя запуска, уникальное в прогоне: так называются его каталоги во временном каталоге.
	Name   string
	Model  string
	Effort string
	// Thinking — модель отдаёт сводку своих размышлений: она приходит в Block.Thinking событий хода. Без него
	// размышлений в потоке нет.
	Thinking bool
	// ProjectInstructions — агент читает инструкции проекта (AGENTS.md, CLAUDE.md).
	ProjectInstructions bool
	Allow               []string // команды, которые агенту можно запускать: префиксы из слов, `git show`
	// SystemPromptFile — файл, текст которого дописывается к системному промпту;
	// пусто — не дописывается. У запусков с одним файлом общий префикс кэша промпта.
	SystemPromptFile string
	Prompt           string
	Env              []string // переменные сверх окружения драйвера
	Skills           []string // скиллы запуска из каталога скиллов прогона
	Cwd              string
	// Schema — файл JSON Schema итогового ответа; пусто — ответ свободным текстом.
	Schema string
	// Sandbox — песочница запуска; нулевое значение — строгая.
	Sandbox Sandbox
	Timeout time.Duration
	// SessionDir — куда сохранить журнал сессии для её продолжения; пусто — не сохранять.
	SessionDir string
	// Socket — сокет команд агента (broker.Socket.Path); пусто — команд через брокер нет. Агент находит его в
	// broker.SocketEnv, строгая песочница открывает его каталог — и только этому запуску.
	Socket string
}

// Sandbox — песочница запуска. Нулевое значение — строгая песочница: агент читает только рабочий каталог, скиллы
// запуска, каталог сокета команд, Readable и Settings.Readable драйвера, сети нет, unix-сокеты открыты.
type Sandbox struct {
	Readable []string
	// ReadOnlyCLI — ослабление: вместо строгой песочницы режим CLI только для чтения. Агент читает любой путь, а
	// команды Allow исполняются по правилам разрешений CLI вне его песочницы, с сетью.
	ReadOnlyCLI bool
}

// Paths — что открыто на чтение: рабочий каталог первым, Readable, скиллы запуска и каталог сокета команд.
func (s Sandbox) Paths(a Args, runSkills string) []string {
	paths := append([]string{a.Cwd}, s.Readable...)
	for _, name := range a.Skills {
		paths = append(paths, filepath.Join(runSkills, "skills", name))
	}
	if a.Socket != "" {
		paths = append(paths, filepath.Dir(a.Socket))
	}
	return paths
}
