package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/hakastein/smith/logx"
)

const silenceReport = 30 * time.Second

// Начало ответа модели: с этого момента кэш промпта, записанный запросом,
// доступен соседним запросам.
func isResponseStart(e *Event) bool { return e.Start || e.Turn != nil }

// Stream — поток этапа: его события и сырой вывод процесса строками.
type Stream interface {
	Write(e *Event)
	Raw(line []byte)
	End()
}

// Telemetry — лог и поток этапа по его событиям, статистика и итог в лог.
type Telemetry struct {
	mu         sync.Mutex
	name       string
	cwd        string
	skills     []string
	commands   []string
	stream     Stream
	events     []*Event
	lastShown  string
	heartbeat  *time.Ticker
	stop       chan struct{}
	responding chan struct{}
	responded  sync.Once
	traffic    *Traffic
}

// NewTelemetry — телеметрия этапа name. commands и skills — команды и скиллы метода этапа: статистика называет
// их и тогда, когда агент к ним не обращался. Телеметрию закрывает Close или Finish, когда этап закончен.
func NewTelemetry(name string, commands, skills []string, cwd string, stream Stream) *Telemetry {
	t := &Telemetry{name: name, cwd: cwd, skills: skills, commands: commands, stream: stream, responding: make(chan struct{}),
		traffic: newTraffic(), stop: make(chan struct{})}
	startedAt := time.Now()
	t.heartbeat = time.NewTicker(silenceReport)
	go func(ticker *time.Ticker, stop chan struct{}) {
		for {
			select {
			case <-ticker.C:
				t.Logf("%sс без единого события — %s", seconds(time.Since(startedAt).Milliseconds()), t.traffic.Describe())
			case <-stop:
				return
			}
		}
	}(t.heartbeat, t.stop)
	return t
}

// Logf пишет в лог строку об этапе.
func (t *Telemetry) Logf(format string, args ...any) {
	logx.Logf("этап «%s»: %s", t.name, fmt.Sprintf(format, args...))
}

func (t *Telemetry) stopHeartbeat() {
	if t.heartbeat != nil {
		t.heartbeat.Stop()
		close(t.stop)
		t.heartbeat = nil
	}
}

// Accept записывает событие драйвера в поток и лог этапа и учитывает его в сообщениях о молчании.
func (t *Telemetry) Accept(e *Event) {
	t.traffic.mu.Lock()
	t.traffic.events++
	t.traffic.lastEventType = e.Kind
	t.traffic.mu.Unlock()
	t.record(e)
}

// Unparsed — строка вывода, которую драйвер не разобрал, или иное сообщение о выводе этапа.
func (t *Telemetry) Unparsed(text string) { t.own("stage_raw", text) }

// own записывает собственное событие о процессе этапа: вывод stderr или неразобранную строку.
func (t *Telemetry) own(kind, text string) {
	e := &Event{Kind: kind, Stderr: text}
	if kind == "stage_raw" {
		e = &Event{Kind: kind, Unparsed: text}
	}
	t.record(e)
}

func (t *Telemetry) record(event *Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if isResponseStart(event) {
		t.markResponding()
	}
	t.stopHeartbeat()
	t.stream.Write(event)
	t.events = append(t.events, event)
	// Лог раскрашен всегда, и вне терминала.
	shown, ok := formatEvent(event, true, t.cwd, t.name)
	if !ok || shown == "" || shown == t.lastShown {
		return
	}
	t.lastShown = shown
	logx.Stamped(shown)
}

// Responding закрывается, когда модель начала отвечать этапу, — с этого момента кэш промпта, записанный его
// запросом, доступен соседним запросам, — или когда этап закончился, не дойдя до ответа: ждущий не висит.
func (t *Telemetry) Responding() <-chan struct{} { return t.responding }

func (t *Telemetry) markResponding() { t.responded.Do(func() { close(t.responding) }) }

// Close — этап закончен: поток закрыт, сообщения о молчании остановлены.
func (t *Telemetry) Close() {
	t.mu.Lock()
	t.stopHeartbeat()
	t.mu.Unlock()
	t.stream.End()
	t.markResponding()
}

// Finish — этап закончен исходом o: статистика и итог в лог.
func (t *Telemetry) Finish(o *Outcome) {
	t.Close()
	for _, line := range statsFrom(t.events, t.cwd, t.skills, t.commands).Lines() {
		logx.Logf("[%s] %s", t.name, line)
	}
	outcome := "завершён"
	if !o.OK {
		detail := ""
		if o.Result == nil {
			code := "?"
			if o.ExitCode != nil {
				code = fmt.Sprint(*o.ExitCode)
			}
			if o.Signal != "" {
				code += ", " + o.Signal
			}
			detail = " [exit " + code + "]"
		}
		outcome = "не выполнен (" + o.Reason + ")" + detail
	}
	logx.Logf("этап «%s» %s: %sс, %s", t.name, outcome, seconds(o.DurationMs), FormatCost(o))
	if !o.OK {
		t.Logf("%s", t.traffic.Describe())
	}
}

// FormatCost — цена этапа. Токены, восстановленные из потока убитого этапа, —
// оценка снизу; цену, которую драйвер не назвал, не выдумываем.
func FormatCost(o *Outcome) string {
	if o.UsageApprox || o.Cost == nil {
		return "$?"
	}
	return fmt.Sprintf("$%.2f", *o.Cost)
}

func seconds(ms int64) string { return fmt.Sprint((ms + 500) / 1000) }
