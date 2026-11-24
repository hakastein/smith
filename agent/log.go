package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// Shorten — текст в одну строку не длиннее limit символов.
func Shorten(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if r := []rune(flat); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return flat
}

func jsonText(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// relativeToCwd — путь от cwd, если он внутри него.
func relativeToCwd(path, cwd string) string {
	if cwd != "" && strings.HasPrefix(path, cwd) {
		return strings.TrimPrefix(path[len(cwd):], "/")
	}
	return path
}

var commandTokens = regexp.MustCompile(`(?:\s|['"|;&()])+`)

// filesIn — файлы, названные в команде: этап читает и `cat`, и `sed -n`, и
// `grep`, поэтому список прочитанного, собранный по одному Read, показывает
// меньше, чем было. Ошибка файловой системы, кроме несуществующего пути, — сбой.
func filesIn(command, cwd string) []string {
	var found []string
	for _, token := range commandTokens.Split(command, -1) {
		if token == "" || strings.HasPrefix(token, "-") {
			continue
		}
		path := token
		if !filepath.IsAbs(token) {
			path = filepath.Join(cwd, token)
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			panic(err)
		}
		if info.Mode().IsRegular() {
			found = append(found, relativeToCwd(path, cwd))
		}
	}
	return found
}

func sessionLine(s *Session) string {
	model := s.Model
	if model == "" {
		model = "модель не названа"
	}
	line := fmt.Sprintf("сессия: %s, инструментов %d", model, s.Tools)
	if len(s.MCP) > 0 {
		return line + ", MCP " + strings.Join(s.MCP, " ")
	}
	return line + ", MCP нет"
}

func describeCall(input map[string]any, cwd string) string {
	var parts []string
	if command, ok := input["command"].(string); ok {
		parts = append(parts, command)
	}
	for _, k := range slices.Sorted(maps.Keys(input)) {
		if k == "command" {
			continue
		}
		text, ok := input[k].(string)
		if ok {
			text = relativeToCwd(text, cwd)
		} else {
			text = jsonText(input[k])
		}
		parts = append(parts, k+"="+text)
	}
	return strings.Join(parts, " ")
}

// line — строка лога о событии потока: вид строки (атрибут event записи), текст и отказ — у ответа на вызов.
type line struct {
	kind, text string
	failed     bool
	parent     string // Event.Parent: строка о субагенте
}

func (l line) level() slog.Level {
	if l.failed || l.kind == "failed" || l.kind == "stderr" || l.kind == "unparsed" {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func (l line) attrs() []slog.Attr {
	attrs := []slog.Attr{slog.String("event", l.kind)}
	if l.kind == "reply" {
		attrs = append(attrs, slog.Bool("error", l.failed))
	}
	if l.parent != "" {
		attrs = append(attrs, slog.String("subagent", l.parent))
	}
	return attrs
}

// eventLines — строки лога о событии потока; пусто — показывать нечего. Вид строки: failed — ошибка драйвера,
// session, note, stderr, unparsed — строка вывода не разобрана, call — вызов инструмента, text — текст модели,
// thinking, reply — ответ инструмента на вызов.
func eventLines(e *Event, cwd string) []line {
	if e.Failed {
		return []line{{kind: "failed", text: "ошибка от драйвера: " + Shorten(string(e.Raw), 1500), parent: e.Parent}}
	}
	var lines []line
	if e.Session != nil {
		lines = append(lines, line{kind: "session", text: sessionLine(e.Session)})
	}
	if e.Note != "" {
		lines = append(lines, line{kind: "note", text: e.Note})
	}
	if text := strings.TrimSpace(e.Stderr); text != "" {
		lines = append(lines, line{kind: "stderr", text: "stderr: " + text})
	}
	if strings.TrimSpace(e.Unparsed) != "" {
		lines = append(lines, line{kind: "unparsed", text: "вывод не разобран: " + Shorten(e.Unparsed, 500)})
	}
	if e.Turn != nil {
		for _, b := range e.Turn.Blocks {
			switch {
			case b.Call != nil:
				lines = append(lines, line{kind: "call", text: b.Call.Name + ": " + describeCall(b.Call.Input, cwd)})
			case strings.TrimSpace(b.Text) != "":
				lines = append(lines, line{kind: "text", text: strings.TrimSpace(b.Text)})
			case strings.TrimSpace(b.Thinking) != "":
				lines = append(lines, line{kind: "thinking", text: "размышление: " + Shorten(b.Thinking, 400)})
			}
		}
	}
	for _, r := range e.Replies {
		lines = append(lines, line{kind: "reply", text: Shorten(r.Text, 200), failed: r.IsError})
	}
	for i := range lines {
		lines[i].parent = e.Parent
	}
	return lines
}

// stats — статистика этапа по его событиям.
type stats struct {
	cwd        string
	skills     map[string]bool
	commands   []string
	callNames  []string
	calls      map[string]int
	files      map[string]bool
	errors     int
	messages   map[string]bool
	looseTurns int
}

// statsFrom — статистика событий этапа.
func statsFrom(events []*Event, cwd string, skills, commands []string) *stats {
	s := &stats{cwd: cwd, skills: map[string]bool{}, commands: commands, calls: map[string]int{}, files: map[string]bool{},
		messages: map[string]bool{}}
	for _, name := range skills {
		s.skills[name] = true
	}
	for _, e := range events {
		s.add(e)
	}
	return s
}

func (s *stats) add(e *Event) {
	if e.Turn != nil {
		switch {
		case e.Parent != "": // ходы субагентов — не ходы этапа
		case e.Turn.MessageID != "":
			s.messages[e.Turn.MessageID] = true
		default:
			s.looseTurns++
		}
		for _, b := range e.Turn.Blocks {
			if b.Call != nil {
				s.addCall(b.Call)
			}
		}
	}
	for _, r := range e.Replies {
		if r.IsError {
			s.errors++
		}
	}
}

func (s *stats) addCall(c *Call) {
	if _, ok := s.calls[c.Name]; !ok {
		s.callNames = append(s.callNames, c.Name)
	}
	s.calls[c.Name]++
	if c.File != "" {
		s.files[relativeToCwd(c.File, s.cwd)] = true
	}
	if c.Command != "" {
		for _, f := range filesIn(c.Command, s.cwd) {
			s.files[f] = true
		}
	}
	if c.Skill != "" {
		s.skills[c.Skill] = true
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// attrs — статистика атрибутами записи: вызовы инструментов по убыванию числа, отказы, скиллы, команды, ходы и
// прочитанные файлы.
func (s *stats) attrs() []slog.Attr {
	names := slices.Clone(s.callNames)
	sort.SliceStable(names, func(i, j int) bool { return s.calls[names[i]] > s.calls[names[j]] })
	tools := make([]slog.Attr, len(names))
	for i, n := range names {
		tools[i] = slog.Int(n, s.calls[n])
	}
	return []slog.Attr{{Key: "tools", Value: slog.GroupValue(tools...)}, slog.Int("errors", s.errors),
		slog.Any("skills", sortedKeys(s.skills)), slog.Any("commands", s.commands),
		slog.Int("turns", len(s.messages)+s.looseTurns), slog.Any("files", sortedKeys(s.files))}
}
