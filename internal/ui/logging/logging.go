// Package logging implements the activity-logging screen: pick a subject,
// adjust hours (±0.5 via arrows / vi-keys), confirm completion and persist.
package logging

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"etrack/internal/storage"
	"etrack/internal/ui/theme"
)

type field int

const (
	fieldSubject field = iota
	fieldHours
	fieldNote
	fieldCount
)

// Model is the logging sub-model. The root model drives it through
// Update(msg) → (Model, tea.Cmd, done bool).
type Model struct {
	db     *storage.DB
	now    time.Time
	width  int
	height int

	subjects []storage.Subject
	planned  map[string]float64 // today's plan per subject
	cursor   int                // selected subject row
	focus    field
	hours    float64
	note     textinput.Model
	err      string
	savedMsg string

	busy  bool
	done_ bool
}

type loadDoneMsg struct {
	subjects []storage.Subject
	planned  map[string]float64
	logged   map[string]float64
	err      error
}

type saveDoneMsg struct {
	entry storage.Entry
	err   error
}

// New builds a fresh logging session for today.
func New(db *storage.DB, now time.Time) Model {
	ni := textinput.New()
	ni.Placeholder = "заметка (необязательно)"
	ni.CharLimit = 80
	ni.Width = 36
	return Model{db: db, now: now, note: ni, focus: fieldHours}
}

// SetSize propagates geometry.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// Init loads subjects + today's plan asynchronously.
func (m Model) Init() tea.Cmd {
	db, now := m.db, m.now
	return func() tea.Msg {
		subs, err := db.Subjects()
		if err != nil {
			return loadDoneMsg{err: err}
		}
		planned, err := db.PlannedHoursOn(now)
		if err != nil {
			return loadDoneMsg{err: err}
		}
		logged, err := db.LoggedHoursOn(now)
		if err != nil {
			return loadDoneMsg{err: err}
		}
		return loadDoneMsg{subjects: subs, planned: planned, logged: logged}
	}
}

// Update processes messages; returns done=true when the session ends.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case loadDoneMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil, false
		}
		m.subjects = msg.subjects
		m.planned = msg.planned
		// Preselect the subject with the biggest remaining gap today.
		best, bestGap := 0, -1.0
		for i, s := range m.subjects {
			gap := msg.planned[s.Name] - msg.logged[s.Name]
			if gap > bestGap {
				best, bestGap = i, gap
			}
		}
		if len(m.subjects) > 0 {
			m.cursor = clampI(best, 0, len(m.subjects)-1)
			m.hours = defaultHours(m.planned[m.subjects[m.cursor].Name])
		}
		return m, nil, false

	case saveDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil, false
		}
		m.savedMsg = fmt.Sprintf("записано %.2f ч · %s", msg.entry.Hours, msg.entry.SubjectName)
		if msg.entry.Completed {
			m.savedMsg += " · цель по предмету закрыта ✔"
		}
		// Move to next incomplete subject if any.
		m.advanceCursor()
		return m, nil, false

	case tea.KeyMsg:
		if m.note.Focused() && msg.String() != "esc" && msg.String() != "tab" &&
			msg.String() != "shift+tab" && msg.String() != "enter" {
			var cmd tea.Cmd
			m.note, cmd = m.note.Update(msg)
			return m, cmd, false
		}
		return m.handleKey(msg)
	}
	return m, nil, false
}

func (m Model) handleKey(km tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := km.String()
	switch key {
	case "esc":
		return m, nil, true
	case "tab":
		m.cycleFocus(1)
		return m, nil, false
	case "shift+tab":
		m.cycleFocus(-1)
		return m, nil, false

	case "up", "k":
		if m.focus == fieldSubject {
			m.moveCursor(-1)
		} else {
			m.adjust(+0.5)
		}
		return m, nil, false
	case "down", "j":
		if m.focus == fieldSubject {
			m.moveCursor(+1)
		} else {
			m.adjust(-0.5)
		}
		return m, nil, false
	case "left", "h":
		m.adjust(-0.5)
		return m, nil, false
	case "right", "l":
		m.adjust(+0.5)
		return m, nil, false
	case "+", "=":
		m.adjust(+0.5)
		return m, nil, false
	case "-":
		m.adjust(-0.5)
		return m, nil, false
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// quick set whole hours when not navigating
		if m.focus != fieldSubject {
			m.hours = float64(int(rune(key[0]) - '0'))
		}
		return m, nil, false
	case "n":
		m.focus = fieldNote
		m.note.Focus()
		return m, nil, false
	case "enter":
		return m.save()
	}
	return m, nil, false
}

func (m *Model) cycleFocus(d int) {
	m.note.Blur()
	next := (int(m.focus) + d + int(fieldCount)) % int(fieldCount)
	m.focus = field(next)
	if m.focus == fieldNote {
		m.note.Focus()
	}
}

func (m *Model) moveCursor(d int) {
	n := len(m.subjects)
	if n == 0 {
		return
	}
	m.cursor = clampI(m.cursor+d, 0, n-1)
	m.hours = defaultHours(m.planned[m.subjects[m.cursor].Name])
}

func (m *Model) advanceCursor() {
	n := len(m.subjects)
	if n == 0 {
		return
	}
	for step := 1; step <= n; step++ {
		i := (m.cursor + step) % n
		name := m.subjects[i].Name
		if m.planned[name] > m.loggedToday(name)+1e-9 {
			m.cursor = i
			m.hours = defaultHours(m.planned[name] - m.loggedToday(name))
			return
		}
	}
	m.hours = 0
}

func (m Model) loggedToday(name string) float64 { return 0 } // recomputed on reload

func (m *Model) adjust(d float64) {
	m.hours = clampF(m.hours+d, 0, 16)
}

func (m Model) save() (Model, tea.Cmd, bool) {
	if m.busy {
		return m, nil, false
	}
	if len(m.subjects) == 0 {
		m.err = "нет предметов — сначала добавь их в настройках"
		return m, nil, false
	}
	if m.hours <= 0 {
		m.err = "часы должны быть больше нуля (↑/↓ или −/+)"
		return m, nil, false
	}
	m.err = ""
	m.busy = true
	subj := m.subjects[m.cursor]
	hours := m.hours
	note := strings.TrimSpace(m.note.Value())
	db, now := m.db, m.now
	cmd := func() tea.Msg {
		e := &storage.Entry{Date: now, SubjectName: subj.Name, Hours: hours, Note: note}
		if err := db.LogEntry(e); err != nil {
			return saveDoneMsg{err: err}
		}
		return saveDoneMsg{entry: *e}
	}
	return m, cmd, false
}

// Summary describes the just-finished session for the root toast.
func (m Model) Summary() string {
	if m.savedMsg != "" {
		return m.savedMsg + " — план пересчитан"
	}
	return "журнал обновлён"
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	_ = clampI(m.width-8, 44, 96)
	head := lipgloss.JoinHorizontal(lipgloss.Center,
		theme.Brand.Render(" ETrack "),
		"  ", theme.Title.Render("Журнал занятий"),
		"  ", theme.Subtitle.Render(m.now.Format("02.01.2006")),
	)

	// Subject list column.
	const listW = 34
	var lb strings.Builder
	lb.WriteString(theme.StatLabel.Render("ПРЕДМЕТ · ПЛАН НА СЕГОДНЯ") + "\n")
	if len(m.subjects) == 0 {
		lb.WriteString(theme.FaintText.Render("список пуст"))
	}
	for i, s := range m.subjects {
		pl := m.planned[s.Name]
		mark, st := "  ", lipgloss.NewStyle().Foreground(theme.Text)
		if i == m.cursor {
			mark = "> "
			st = lipgloss.NewStyle().Bold(true).Foreground(theme.Accent)
		}
		if m.focus == fieldSubject && i == m.cursor {
			st = st.Underline(true)
		}
		txt := fmt.Sprintf("%s%-20s %4.1f ч", mark, trunc(s.Name, 20), pl)
		lb.WriteString(st.Render(txt) + "\n")
	}

	// Editing column.
	var eb strings.Builder
	name := "—"
	if len(m.subjects) > 0 {
		name = m.subjects[m.cursor].Name
	}
	eb.WriteString(theme.StatLabel.Render("ЗАПИСЬ ЗАНЯТИЯ") + "\n\n")
	eb.WriteString(label(m.focus == fieldSubject, "предмет ") + value(name) + "\n")

	bar := hourDial(m.hours, maxF(m.planned[name], 8))
	eb.WriteString(label(m.focus == fieldHours, "часы  ") +
		theme.StatValue.Render(fmt.Sprintf("%4.1f ч", m.hours)) + "  " + bar + "\n")
	eb.WriteString(theme.FaintText.Render("↑/↓ или −/+ шаг 0.5 ч · цифры 1–9 — быстро") + "\n\n")

	eb.WriteString(label(m.focus == fieldNote, "заметка ") + m.note.View() + "\n\n")
	if m.busy {
		eb.WriteString(theme.Subtitle.Render("сохраняю…") + "\n")
	} else {
		eb.WriteString(theme.Success.Render("enter — сохранить") +
			theme.Help.Render("  ·  esc — выйти на дашборд") + "\n")
	}
	if m.err != "" {
		eb.WriteString("\n" + theme.Error.Render("⚠ "+m.err) + "\n")
	}
	if m.savedMsg != "" && !m.busy {
		eb.WriteString("\n" + theme.Success.Render("✔ "+m.savedMsg) + "\n")
	}

	left := lipgloss.NewStyle().Width(listW).Padding(0, 1).
		Border(lipgloss.RoundedBorder()).BorderForeground(theme.PanelEdge).Render(lb.String())
	right := lipgloss.NewStyle().Padding(0, 1).
		Border(lipgloss.RoundedBorder()).BorderForeground(theme.PanelEdge).Render(eb.String())

	body := lipgloss.JoinVertical(lipgloss.Left, left, right)
	return head + "\n\n" + lipgloss.PlaceHorizontal(maxI(m.width, listW+lipgloss.Width(right)+4), lipgloss.Left, body)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hourDial(hours, scale float64) string {
	blocks := int(hours / 0.5)
	if blocks > 20 {
		blocks = 20
	}
	filled := lipgloss.NewStyle().Foreground(theme.Green).Render(strings.Repeat("▇", blocks))
	empty := lipgloss.NewStyle().Foreground(theme.GridEmpty).Render(strings.Repeat("▁", maxI(20-blocks, 0)))
	return filled + empty
}

func label(active bool, s string) string {
	if active {
		return lipgloss.NewStyle().Bold(true).Foreground(theme.Accent).Render("▶ " + s)
	}
	return theme.MutedText.Render("   " + s)
}

func value(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Text).Bold(true).Render(s)
}

func defaultHours(planned float64) float64 {
	if planned >= 0.5 {
		return roundQ(planned)
	}
	return 1.0
}

func roundQ(v float64) float64 {
	q := float64(int64(v*4+0.5)) / 4
	return q
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var _ = sort.Strings
