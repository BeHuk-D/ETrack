// Package settings implements the "Adjust" screen: a keyboard-driven form
// over the user profile (score targets, study slot, weekend policy) plus
// maintenance actions (full re-plan, reset). Saving here triggers a plan
// regeneration through the injected RegenFunc, keeping the scheduler the
// single source of truth for daily hours.
package settings

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"etrack/internal/storage"
	"etrack/internal/ui/theme"
)

// RegenFunc asks the root model to rebuild and persist the whole schedule.
type RegenFunc func() tea.Cmd

// ---------------------------------------------------------------------------
// Form rows
// ---------------------------------------------------------------------------

type rowKind int

const (
	rowTotalInput rowKind = iota // editable number (total target score)
	rowSlot                      // Morning / Afternoon / Evening carousel
	rowToggle                    // bool switch
	rowSubjectTarget             // editable per-subject score
	rowAction                    // Enter fires a command
)

type row struct {
	kind     rowKind
	label    string
	value    string  // rendered current value
	focused  bool    // input row currently being edited
	editBuf  string  // raw text while editing numeric rows
	toggleOn *bool   // pointer into the profile for toggle rows
	slotIdx  *int    // index into storage.AllStudySlots
	subjName string  // for rowSubjectTarget
	action   string  // identifier for rowAction
}

type actionMsg struct{ id string }

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type Model struct {
	db  *storage.DB
	reg RegenFunc

	rows    []row
	cursor  int
	width   int
	height  int
	editing bool // an input row owns the keyboard

	status string
	err    error
}

// New loads the profile and builds the form.
func New(db *storage.DB, regen RegenFunc) Model {
	m := Model{db: db, reg: regen}
	m.load()
	return m
}

// SetSize propagates window geometry.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

func (m *Model) load() {
	m.rows = m.rows[:0]
	p, err := m.db.GetProfile()
	if err != nil {
		m.err = err
		return
	}
	m.err = nil

	subs, _ := m.db.Subjects()

	// Total-score mode only makes sense in "total" profiles, but we always
	// show it so the user can switch strategies by raising individual scores.
	m.rows = append(m.rows, row{
		kind: rowTotalInput, label: "Целевой суммарный балл",
		value: strconv.Itoa(p.TotalTargetScore),
	})

	slotIdx := slotIndexOf(p.StudySlot)
	m.rows = append(m.rows, row{
		kind: rowSlot, label: "Предпочтительное время занятий",
		slotIdx: &slotIdx, value: storage.AllStudySlots[slotIdx].Label(),
	})

	wk, sat, sun := p.StudyWeekends, p.SatAvailable, p.SunAvailable
	m.rows = append(m.rows,
		row{kind: rowToggle, label: "Заниматься по выходным", toggleOn: &wk, value: onOff(wk)},
		row{kind: rowToggle, label: "Суббота доступна", toggleOn: &sat, value: onOff(sat)},
		row{kind: rowToggle, label: "Воскресенье доступна", toggleOn: &sun, value: onOff(sun)},
	)

	for i := range subs {
		s := subs[i]
		m.rows = append(m.rows, row{
			kind: rowSubjectTarget, label: fmt.Sprintf("Балл · %s", s.Name),
			subjName: s.Name, value: strconv.Itoa(s.TargetScore),
		})
	}

	m.rows = append(m.rows,
		row{kind: rowAction, label: "Сохранить и пересчитать план", action: "save"},
		row{kind: rowAction, label: theme.Warn.Render("Полный сброс данных"), action: "reset"},
	)
	if m.cursor >= len(m.rows) {
		m.cursor = 0
	}
}

func slotIndexOf(s storage.StudySlot) int {
	for i, v := range storage.AllStudySlots {
		if v == s {
			return i
		}
	}
	return 2 // evening default
}

func onOff(b bool) string {
	if b {
		return theme.Success.Render("вкл")
	}
	return theme.MutedText.Render("выкл")
}

// accentText renders an inline editing buffer in the accent colour.
func accentText(s string) string { return theme.Key.Render(s) }

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Init() tea.Cmd { return nil }

// Update returns the new model, a command, and whether the root should
// regenerate the schedule (true after a successful save).
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case actionMsg:
		switch msg.id {
		case "save":
			replan := m.persist()
			m.status = "Настройки сохранены"
			return m, m.reg(), replan
		case "reset":
			if err := m.db.ResetAll(); err != nil {
				m.err = err
			} else {
				m.status = "Данные сброшены — перезапустите мастер"
				m.load()
			}
			return m, nil, false
		}
		return m, nil, false

	case tea.KeyMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j", "tab":
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
		case "shift+tab":
			if m.cursor > 0 {
				m.cursor--
			}
		case "left", "h":
			m.adjust(-1)
		case "right", "l", " ":
			m.adjust(+1)
		case "enter":
			r := &m.rows[m.cursor]
			switch r.kind {
			case rowAction:
				return m, func() tea.Msg { return actionMsg{r.action} }, false
			case rowTotalInput, rowSubjectTarget:
				m.editing = true
				r.focused = true
				r.editBuf = digitsOnly(r.value)
			default:
				m.adjust(+1)
			}
		case "s":
			return m, func() tea.Msg { return actionMsg{"save"} }, false
		}
	}
	return m, nil, false
}

func (m Model) updateEditing(km tea.KeyMsg) (Model, tea.Cmd, bool) {
	r := &m.rows[m.cursor]
	switch km.String() {
	case "enter":
		n, err := strconv.Atoi(r.editBuf)
		if err == nil && n >= 0 && n <= 100 {
			r.value = strconv.Itoa(n)
		}
		r.focused, m.editing = false, false
	case "esc", "ctrl+c":
		r.focused, m.editing = false, false
	case "backspace":
		if len(r.editBuf) > 0 {
			r.editBuf = r.editBuf[:len(r.editBuf)-1]
		}
	default:
		if d := digitsOnly(km.String()); d != "" && len(r.editBuf) < 3 {
			r.editBuf += d
		}
	}
	return m, nil, false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// adjust mutates the row under the cursor by one step (direction ±1).
func (m *Model) adjust(dir int) {
	r := &m.rows[m.cursor]
	switch r.kind {
	case rowToggle:
		if r.toggleOn != nil {
			*r.toggleOn = !*r.toggleOn
			r.value = onOff(*r.toggleOn)
		}
	case rowSlot:
		if r.slotIdx != nil {
			n := len(storage.AllStudySlots)
			*r.slotIdx = (*r.slotIdx + dir + n) % n
			r.value = storage.AllStudySlots[*r.slotIdx].Label()
		}
	case rowTotalInput, rowSubjectTarget:
		v, _ := strconv.Atoi(digitsOnly(r.value))
		v += dir * 5
		if v < 0 {
			v = 0
		}
		if v > 100 && r.kind == rowSubjectTarget {
			v = 100
		}
		r.value = strconv.Itoa(v)
	}
}

// persist writes the form back to the DB; returns true if the caller must
// regenerate the schedule afterwards.
func (m *Model) persist() bool {
	p, err := m.db.GetProfile()
	if err != nil {
		m.err = err
		return false
	}
	changed := false
	for _, r := range m.rows {
		switch r.kind {
		case rowTotalInput:
			v, _ := strconv.Atoi(digitsOnly(r.value))
			if v != p.TotalTargetScore {
				p.TotalTargetScore = v
				changed = true
			}
		case rowSlot:
			if p.StudySlot != storage.AllStudySlots[*r.slotIdx] {
				p.StudySlot = storage.AllStudySlots[*r.slotIdx]
				changed = true
			}
		case rowToggle:
		}
	}
	// Weekend flags live behind pointer fields captured at load time; read
	// them from the corresponding rows by label match.
	readToggle := func(label string) *bool {
		for i := range m.rows {
			if m.rows[i].label == label {
				return m.rows[i].toggleOn
			}
		}
		return nil
	}
	if b := readToggle("Заниматься по выходным"); b != nil && *b != p.StudyWeekends {
		p.StudyWeekends, changed = *b, true
	}
	if b := readToggle("Суббота доступна"); b != nil && *b != p.SatAvailable {
		p.SatAvailable, changed = *b, true
	}
	if b := readToggle("Воскресенье доступна"); b != nil && *b != p.SunAvailable {
		p.SunAvailable, changed = *b, true
	}

	if err := m.db.SaveProfile(p); err != nil {
		m.err = err
		return false
	}
	for _, r := range m.rows {
		if r.kind != rowSubjectTarget {
			continue
		}
		s, err := m.db.SubjectByName(r.subjName)
		if err != nil {
			continue
		}
		v, _ := strconv.Atoi(digitsOnly(r.value))
		if v != s.TargetScore {
			s.TargetScore = v
			if err := m.db.UpdateSubject(s); err != nil {
				m.err = err
			}
			changed = true
		}
	}
	return changed || true // any save revalidates the plan
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	title := theme.Title.Render("Настройки и корректировка")
	var lines []string
	for i, r := range m.rows {
		cursor := "  "
		st := theme.StepTodo
		switch {
		case i == m.cursor && r.focused:
			cursor, st = "> ", theme.StepActive
		case i == m.cursor:
			cursor, st = "> ", theme.StepActive
		}
		val := r.value
		if r.focused {
			val = accentText(r.editBuf + "▏")
		}
		lines = append(lines, st.Render(fmt.Sprintf("%-42s %s", cursor+r.label, val)))
	}
	body := strings.Join(lines, "\n")

	footer := theme.Help.Render("↑/↓ или tab — навигация · ←/→ или space — изменить · enter — редактировать · s — сохранить")

	boxW := min(78, max(40, m.width-4))
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", footer)
	view := theme.PanelActive.Width(boxW).Render(content)
	if m.err != nil {
		view += "\n" + theme.Error.Render(fmt.Sprintf(" ⚠ %v ", m.err))
	} else if m.status != "" {
		view += "\n" + theme.Success.Render(" ✔ "+m.status+" ")
	}
	return view
}
