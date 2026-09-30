// Package ui holds the root Bubble Tea model: the multi-screen router,
// shared commands and cross-screen messages. Leaf screens live in
// sub-packages (onboarding, dashboard, logging, settings).
package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"etrack/internal/schedule"
	"etrack/internal/storage"
	"etrack/internal/ui/dashboard"
	"etrack/internal/ui/logging"
	"etrack/internal/ui/onboarding"
	"etrack/internal/ui/settings"
	"etrack/internal/ui/theme"
)

// Screen identifiers for the view router.
type Screen int

const (
	ScreenOnboarding Screen = iota
	ScreenDashboard
	ScreenLogging
	ScreenSettings
)

// ---------------------------------------------------------------------------
// Shared commands & messages
// ---------------------------------------------------------------------------

// Tick fires once per minute; used to roll over "today" at midnight.
type tickMsg time.Time

func tickEvery() tea.Cmd {
	return tea.Tick(time.Minute, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// errMsg surfaces storage failures non-fatally.
type errMsg struct{ err error }

// RegenerateCmd recomputes the whole plan from DB state and persists it.
// This is the single entry point of the recalculation engine — called after
// onboarding, every logging session, feedback answers and settings changes.
func RegenerateCmd(db *storage.DB) tea.Cmd {
	return func() tea.Msg {
		p, err := db.GetProfile()
		if err != nil {
			return errMsg{err}
		}
		subs, err := db.Subjects()
		if err != nil {
			return errMsg{err}
		}
		if len(subs) == 0 {
			return regeneratedMsg{}
		}
		in := make([]schedule.SubjectInput, 0, len(subs))
		for _, s := range subs {
			in = append(in, schedule.SubjectInput{
				Name:        s.Name,
				ExamDate:    s.ExamDate,
				Difficulty:  s.Difficulty,
				TargetScore: s.TargetScore,
			})
		}
		res := schedule.Generate(schedule.Params{
			From:      time.Now(),
			Subjects:  in,
			Weekends:  p.StudyWeekends,
			Sat:       p.SatAvailable,
			Sun:       p.SunAvailable,
			Intensity: p.Intensity,
		})
		var plans []storage.DayPlan
		for key, m := range res.Plans {
			d, err := time.Parse("2006-01-02", key)
			if err != nil {
				continue
			}
			plans = append(plans, storage.DayPlan{Date: d, Subject: m})
		}
		if err := db.ReplaceSchedule(plans); err != nil {
			return errMsg{err}
		}
		for name, h := range res.HoursPerWeek {
			if err := db.SetWeeklyLoad(name, h); err != nil {
				return errMsg{err}
			}
		}
		return regeneratedMsg{}
	}
}

type regeneratedMsg struct{}

// ---------------------------------------------------------------------------
// Root model
// ---------------------------------------------------------------------------

type Model struct {
	db  *storage.DB
	now time.Time

	screen  Screen
	spinner spinner.Model
	ready   bool
	width   int
	height  int

	onb  onboarding.Model
	dash dashboard.Model
	log  logging.Model
	set  settings.Model

	feedbackActive bool // 10-day overlay currently shown
	fbChoices      []schedule.PaceChoice
	fbIndex        int

	err     error
	toast   string
	toastAt time.Time
}

// New constructs the root model.
func New(db *storage.DB) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Accent)
	m := Model{db: db, now: time.Now(), screen: ScreenOnboarding, spinner: sp}
	m.onb = onboarding.New(db, onboarding.DoneFunc(m.onOnboardingDone))
	return m
}

// onOnboardingDone is invoked by the wizard after it persisted everything.
func (m *Model) onOnboardingDone() {
	m.screen = ScreenDashboard
	m.dash = dashboard.New(m.db, m.now)
	m.log = logging.New(m.db, m.now)
	m.set = settings.New(m.db, settings.RegenFunc(func() tea.Cmd { return RegenerateCmd(m.db) }))
	m.toast = "План построен. Удачи на ЕГЭ! 🚀"
	m.toastAt = time.Now()
}

// ---------------------------------------------------------------------------
// tea.Model implementation
// ---------------------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Init(), m.onb.Init(), tickEvery())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.onb.SetSize(msg.Width, msg.Height)
		m.dash.SetSize(msg.Width, msg.Height)
		m.log.SetSize(msg.Width, msg.Height)
		m.set.SetSize(msg.Width, msg.Height)
		if !m.dbIsEmpty() && m.screen == ScreenDashboard {
			cmds = append(cmds, m.dash.Reload())
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		// Midnight rollover: refresh "today" everywhere.
		newNow := time.Now()
		if !dashboard.SameDay(newNow, m.now) {
			m.now = newNow
			m.dash = dashboard.New(m.db, m.now)
			m.log = logging.New(m.db, m.now)
			cmds = append(cmds, m.dash.Reload(), RegenerateCmd(m.db))
		}
		return m, tea.Batch(tickEvery(), tea.Batch(cmds...))

	case regeneratedMsg:
		m.feedbackActive = m.checkFeedbackDue()
		if m.screen == ScreenDashboard {
			cmds = append(cmds, m.dash.Reload())
		}
		return m, tea.Batch(cmds...)

	case errMsg:
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		// Global keys first.
		switch msg.String() {
		case "ctrl+c", "q":
			if m.feedbackActive || m.screen == ScreenOnboarding {
				// never quit from modal states with bare q
				if msg.String() == "ctrl+c" {
					return m, tea.Quit
				}
				break
			}
			return m, tea.Quit
		case "ctrl+c":
			return m, tea.Quit
		}
		if m.feedbackActive {
			return m.updateFeedback(msg)
		}
		// Screen switching shortcuts (not while in the wizard).
		if m.screen != ScreenOnboarding {
			switch msg.String() {
			case "1":
				m.screen = ScreenDashboard
				cmds = append(cmds, m.dash.Reload())
			case "2":
				m.screen = ScreenLogging
				m.log = logging.New(m.db, m.now)
			case "3", ",":
				m.screen = ScreenSettings
				m.set.SetSize(m.width, m.height)
			}
		}
	}

	// Route to the active screen.
	switch m.screen {
	case ScreenOnboarding:
		var cmd tea.Cmd
		m.onb, cmd = m.onb.Update(msg)
		cmds = append(cmds, cmd)
	case ScreenDashboard:
		var cmd tea.Cmd
		m.dash, cmd = m.dash.Update(msg)
		cmds = append(cmds, cmd)
		if m.dash.WantLog {
			m.dash.WantLog = false
			m.screen = ScreenLogging
			m.log = logging.New(m.db, m.now)
		}
	case ScreenLogging:
		sub, cmd, done := m.log.Update(msg)
		m.log = sub
		cmds = append(cmds, cmd)
		if done {
			m.screen = ScreenDashboard
			m.toast = m.log.Summary()
			m.toastAt = time.Now()
			cmds = append(cmds, RegenerateCmd(m.db)) // debt-aware redistribution
		}
	case ScreenSettings:
		sub, cmd, regen := m.set.Update(msg)
		m.set = sub
		cmds = append(cmds, cmd)
		if regen {
			m.toast = "Настройки сохранены, план пересчитан"
			m.toastAt = time.Now()
			cmds = append(cmds, RegenerateCmd(m.db), m.dash.Reload())
		}
	}

	// Spinner always animates while busy.
	var spCmd tea.Cmd
	m.spinner, spCmd = m.spinner.Update(msg)
	cmds = append(cmds, spCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) dbIsEmpty() bool {
	subs, err := m.db.Subjects()
	return err != nil || len(subs) == 0
}

// checkFeedbackDue opens the 10-day overlay when the counter passes.
func (m *Model) checkFeedbackDue() bool {
	p, err := m.db.GetProfile()
	if err != nil || !p.Onboarded {
		return false
	}
	if !schedule.ShouldAskFeedback(p.ActiveDays, p.NextFeedbackDay) {
		return false
	}
	m.fbChoices = []schedule.PaceChoice{
		schedule.PaceTooHard, schedule.PaceSlightlyHard, schedule.PaceJustRight,
		schedule.PaceSlightlyEasy, schedule.PaceTooEasy,
	}
	m.fbIndex = 2
	return true
}

func (m Model) updateFeedback(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "up", "k":
		if m.fbIndex > 0 {
			m.fbIndex--
		}
		return m, nil
	case "down", "j":
		if m.fbIndex < len(m.fbChoices)-1 {
			m.fbIndex++
		}
		return m, nil
	case "enter":
		p, err := m.db.GetProfile()
		if err != nil {
			return m, func() tea.Msg { return errMsg{err} }
		}
		choice := m.fbChoices[m.fbIndex]
		newIntensity := schedule.ApplyIntensity(p.Intensity, choice)
		p.Intensity = newIntensity
		p.NextFeedbackDay = schedule.NextFeedbackAfter(p.NextFeedbackDay)
		if err := m.db.SaveProfile(p); err != nil {
			return m, func() tea.Msg { return errMsg{err} }
		}
		if err := m.db.AddFeedback(&storage.Feedback{
			Date: m.now, Pace: km.String(), Intensity: newIntensity,
		}); err != nil {
			return m, func() tea.Msg { return errMsg{err} }
		}
		m.feedbackActive = false
		m.toast = fmt.Sprintf("Темп обновлён: ×%.2f — план пересчитан", newIntensity)
		m.toastAt = time.Now()
		return m, RegenerateCmd(m.db)
	case "esc":
		m.feedbackActive = false // snooze until next cycle
		p, _ := m.db.GetProfile()
		if p != nil {
			p.NextFeedbackDay = schedule.NextFeedbackAfter(p.NextFeedbackDay)
			_ = m.db.SaveProfile(p)
		}
		return m, nil
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if !m.ready {
		return "\n  ETrack прогревается…"
	}

	var body string
	switch m.screen {
	case ScreenOnboarding:
		body = m.onb.View()
	case ScreenDashboard:
		body = m.dash.View()
	case ScreenLogging:
		body = m.log.View()
	case ScreenSettings:
		body = m.set.View()
	}

	if m.feedbackActive {
		body += "\n" + m.feedbackView()
	} else if m.err != nil {
		body += "\n" + theme.Error.Render(fmt.Sprintf(" ⚠ %v ", m.err))
	} else if m.toast != "" && time.Since(m.toastAt) < 8*time.Second {
		body += "\n" + theme.Success.Render(" ✔ "+m.toast+" ")
	}

	footer := m.footer()
	// Pin footer to the bottom without flicker: pad body to height-budget.
	gap := m.height - lipglossHeight(body) - lipglossHeight(footer)
	if gap < 0 {
		gap = 0
	}
	return body + repeatNewline(gap) + footer
}

func (m Model) footer() string {
	tabs := []struct {
		key string
		lbl string
		scr Screen
	}{{"1", "Дашборд", ScreenDashboard}, {"2", "Журнал", ScreenLogging}, {"3", "Настройки", ScreenSettings}}
	left := theme.Brand.Render("ETrack")
	if m.screen != ScreenOnboarding {
		var parts []string
		for _, t := range tabs {
			st := theme.StepTodo
			if t.scr == m.screen {
				st = theme.StepActive
			}
			parts = append(parts, st.Render(fmt.Sprintf("%s %s", t.key, t.lbl)))
		}
		left += "  " + joinSp(parts)
	}
	helpTxt := "ctrl+c — выход"
	switch m.screen {
	case ScreenDashboard:
		helpTxt = "l — записать занятие · r — пересчитать · 1/2/3 — экраны · ctrl+c — выход"
	case ScreenLogging:
		helpTxt = "tab — поля · ↑/↓ −/+ 0.5ч · enter — сохранить и выйти"
	case ScreenSettings:
		helpTxt = "↑/↓ — навигация · ←/→ или space — изменить · s — сохранить"
	}
	right := theme.Help.Render(helpTxt)
	return left + "\n" + right
}

func (m Model) feedbackView() string {
	p, _ := m.db.GetProfile()
	boxW := minI(m.width-4, 72)
	var lines []string
	lines = append(lines, theme.Title.Render("Чек-ин: как тебе темп?"))
	if p != nil {
		lines = append(lines, theme.Subtitle.Render(
			fmt.Sprintf("Пройдено активных дней: %d · текущий множитель: ×%.2f", p.ActiveDays, p.Intensity)))
	}
	lines = append(lines, "")
	for i, c := range m.fbChoices {
		st := theme.StepTodo
		mark := "  "
		if i == m.fbIndex {
			st = theme.StepActive
			mark = "> "
		}
		lines = append(lines, st.Render(mark+c.Label()))
	}
	lines = append(lines, "", theme.Help.Render("↑/↓ выбрать · enter подтвердить · esc пропустить"))
	return theme.PanelActive.Width(boxW).Render(joinNL(lines))
}

// ---------------------------------------------------------------------------
// tiny helpers (avoid extra imports)
// ---------------------------------------------------------------------------

func lipglossHeight(s string) int { return countNL(s) + 1 }
func countNL(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}
func repeatNewline(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '\n'
	}
	return string(b)
}
func joinSp(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
func joinNL(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
