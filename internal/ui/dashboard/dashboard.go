// Package dashboard renders ETrack's main screen: today's goal, streak,
// exam countdown and the GitHub-style activity matrix.
package dashboard

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"etrack/internal/storage"
	"etrack/internal/ui/theme"
)

// SameDay is exported for the root model's midnight-rollover check.
func SameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

type loadDoneMsg struct {
	snap    Snapshot
	planned map[string]float64
	logged  map[string]float64
	streak  int
	grid    []storage.DayTotal
	err     error
}

// Snapshot is the read-only data bundle the dashboard renders.
type Snapshot struct {
	Profile   *storage.Profile
	Subjects  []storage.Subject
	Today     time.Time
	PlanToday map[string]float64
	LogToday  map[string]float64
	Streak    int
	Grid      []storage.DayTotal
}

// Model is the dashboard sub-model.
type Model struct {
	db      *storage.DB
	now     time.Time
	width   int
	height  int
	loading bool
	WantLog bool // set when the user asks to open the logging screen

	snap    Snapshot
	loadErr error
}

// New builds an empty dashboard; call Reload to fetch data.
func New(db *storage.DB, now time.Time) Model {
	return Model{db: db, now: now, loading: true}
}

// SetSize propagates window geometry.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Reload issues an asynchronous DB read so Update never blocks.
func (m Model) Reload() tea.Cmd {
	db, now := m.db, m.now
	return func() tea.Msg {
		p, err := db.GetProfile()
		if err != nil {
			return loadDoneMsg{err: err}
		}
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
		streak, err := db.CurrentStreak(now)
		if err != nil {
			return loadDoneMsg{err: err}
		}
		from := now.AddDate(0, 0, -13*7)
		grid, err := db.DailyTotals(from, now)
		if err != nil {
			return loadDoneMsg{err: err}
		}
		return loadDoneMsg{
			snap:    Snapshot{Profile: p, Subjects: subs, Today: now, PlanToday: planned, LogToday: logged, Streak: streak, Grid: grid},
			streak:  streak,
			grid:    grid,
			planned: planned,
			logged:  logged,
		}
	}
}

// Update handles keys and reload results.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loadDoneMsg:
		m.loading = false
		if msg.err != nil {
			m.loadErr = msg.err
			return m, nil
		}
		m.loadErr = nil
		m.snap = msg.snap
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "l", "L":
			m.WantLog = true
			return m, nil
		case "r", "R":
			m.loading = true
			return m, tea.Batch(m.Reload())
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if m.loading && m.snap.Profile == nil {
		return theme.Subtitle.Render(" загружаю данные… ")
	}
	if m.loadErr != nil {
		return theme.Error.Render(fmt.Sprintf(" ошибка чтения БД: %v ", m.loadErr))
	}
	if m.width < 40 {
		return theme.Warn.Render(" окно слишком узкое — разверни терминал ")
	}

	leftW := clampI(int(float64(m.width)*0.38), 26, 40)
	rightW := maxInt(m.width-leftW-4, 30)

	goal := m.goalPanel(leftW)
	stats := m.statsPanel(leftW)
	left := lipgloss.JoinVertical(lipgloss.Left, goal, "", stats)

	grid := m.gridPanel(rightW)
	subs := m.subjectsPanel(rightW)
	right := lipgloss.JoinVertical(lipgloss.Left, grid, "", subs)

	head := lipgloss.JoinHorizontal(lipgloss.Center,
		theme.Brand.Render(" ETrack "),
		"  ",
		theme.Title.Render("Панель подготовки"),
		"  ",
		theme.Subtitle.Render(m.now.Format("пт, 02 января 2006")),
	)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	return head + "\n\n" + body
}

// goalPanel shows today's plan vs actual with per-subject mini bars.
func (m Model) goalPanel(w int) string {
	var totalPlan, totalLog float64
	names := make([]string, 0, len(m.snap.PlanToday))
	for n, h := range m.snap.PlanToday {
		totalPlan += h
		names = append(names, n)
	}
	for _, h := range m.snap.LogToday {
		totalLog += h
	}
	sort.Strings(names)

	lines := []string{theme.StatLabel.Render("ЦЕЛЬ НА СЕГОДНЯ")}
	if totalPlan == 0 {
		lines = append(lines, "", theme.Faint.Render("на сегодня плана нет — отдых или выходной"))
	} else {
		lines = append(lines,
			theme.StatValue.Render(fmt.Sprintf("%.2f / %.2f ч", totalLog, totalPlan)),
			uiBar(totalLog/max1f(totalPlan), w-6),
			"",
		)
		for _, n := range names {
			pl := m.snap.PlanToday[n]
			lg := m.snap.LogToday[n]
			done := lg+1e-9 >= pl*0.9
			mark := "○"
			st := theme.Muted
			if done {
				mark = "●"
				st = theme.Success
			}
			lines = append(lines, st.Render(fmt.Sprintf("%s %-18s %4.1f/%-4.1f ч", mark, trunc(n, 18), lg, pl)))
		}
	}
	lines = append(lines, "", theme.Help.Render("l — записать занятие"))
	return theme.Panel.Width(w).Render(joinNL(lines))
}

// statsPanel: streak · days left · active days · pace factor.
func (m Model) statsPanel(w int) string {
	nearest := m.nearestExam()
	daysLeft := "-"
	examName := "нет экзаменов"
	if !nearest.ExamDate.IsZero() {
		d := int(nearest.ExamDate.Sub(startOf(m.now)).Hours() / 24)
		daysLeft = fmt.Sprintf("%d", maxInt(d, 0))
		examName = nearest.Name
	}
	fbIn := "?"
	if m.snap.Profile != nil {
		fbIn = fmt.Sprintf("%d", maxInt(m.snap.Profile.NextFeedbackDay-m.snap.Profile.ActiveDays, 0))
	}
	cell := func(val, lbl string) string {
		return lipgloss.NewStyle().Width(w / 2).Align(lipgloss.Center).Render(
			theme.StatValue.Render(val) + "\n" + theme.StatLabel.Render(lbl))
	}
	row1 := lipgloss.JoinHorizontal(lipgloss.Top, cell("🔥 "+fmt.Sprint(m.snap.Streak), "дней подряд"),
		cell(daysLeft, "до «"+trunc(examName, 12)+"»"))
	row2 := lipgloss.JoinHorizontal(lipgloss.Top,
		cell(fmt.Sprint(orZero(m.snap.Profile == nil, 0, m.snap.Profile.ActiveDays)), "активных дней"),
		cell(fmt.Sprintf("×%.2f", orZeroF(m.snap.Profile == nil, 1, profileIntensity(m.snap.Profile))), "темп"))
	return theme.Panel.Width(w).Render(row1 + "\n\n" + row2)
}

func (m Model) gridPanel(w int) string {
	title := theme.StatLabel.Render("АКТИВНОСТЬ · ПОСЛЕДНИЕ 13 НЕДЕЛЬ")
	g := renderHeatGrid(m.snap.Grid, m.now, w)
	return theme.Panel.Width(w).Render(title + "\n\n" + g)
}

func (m Model) subjectsPanel(w int) string {
	lines := []string{theme.StatLabel.Render("ПРЕДМЕТЫ")}
	if len(m.snap.Subjects) == 0 {
		lines = append(lines, theme.Faint.Render(" пока пусто "))
	}
	for _, s := range m.snap.Subjects {
		days := int(s.ExamDate.Sub(startOf(m.now)).Hours() / 24)
		state := theme.Muted
		txt := fmt.Sprintf("%-20s %3d б. ×%.2f  %5.1f ч/нед  ", trunc(s.Name, 20), s.TargetScore, s.Difficulty, s.HoursPerWeek)
		switch {
		case days <= 7:
			state = theme.Error
		case days <= 30:
			state = theme.Warn
		}
		lines = append(lines, state.Render(txt+fmt.Sprintf("экзамен через %d дн.", maxInt(days, 0))))
	}
	return theme.Panel.Width(w).Render(joinNL(lines))
}

func (m Model) nearestExam() storage.Subject {
	var best storage.Subject
	first := true
	for _, s := range m.snap.Subjects {
		if s.ExamDate.Before(m.now) {
			continue
		}
		if first || s.ExamDate.Before(best.ExamDate) {
			best, first = s, false
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// heat grid (local copy keeps packages acyclic; identical rules to ui pkg)
// ---------------------------------------------------------------------------

const (
	cellOn  = "■ "
	cellOff = "░ "
	rows    = 7
)

var heatColors = []lipgloss.AdaptiveColor{
	theme.GridEmpty,
	{Light: "#ACE1E4", Dark: "#0E4429"},
	{Light: "#54D6C6", Dark: "#006D32"},
	{Light: "#26A69A", Dark: "#26A641"},
	{Light: "#0A8043", Dark: "#39D353"},
}

func heatLevel(h float64) int {
	switch {
	case h <= 0:
		return 0
	case h < 1.5:
		return 1
	case h < 3:
		return 2
	case h < 5:
		return 3
	default:
		return 4
	}
}

func renderHeatGrid(days []storage.DayTotal, today time.Time, width int) string {
	const colsMax = 13
	if len(days) == 0 {
		return theme.Faint.Render("нет данных")
	}
	end := startOf(today)
	start := end.AddDate(0, 0, -(colsMax*7 - 1))
	type week struct {
		cells [rows]storage.DayTotal
		has   [rows]bool
	}
	var weeks []week
	idx := map[string]int{}
	for _, d := range days {
		if d.Date.Before(start) || d.Date.After(end) {
			continue
		}
		monday := d.Date.AddDate(0, 0, -int((d.Date.Weekday()+6)%7))
		k := monday.Format("2006-01-02")
		wi, ok := idx[k]
		if !ok {
			weeks = append(weeks, week{})
			wi = len(weeks) - 1
			idx[k] = wi
		}
		r := int((d.Date.Weekday() + 6) % 7)
		weeks[wi].cells[r] = d
		weeks[wi].has[r] = true
	}

	wd := []string{"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}
	var b strings.Builder
	b.WriteString("  ")
	lastMonth := ""
	for _, wk := range weeks {
		lbl := "    "
		for r := 0; r < rows; r++ {
			if wk.has[r] {
				mn := wk.cells[r].Date.Format("01")
				if mn != lastMonth {
					lbl = padStr(mn+"·", 4)
					lastMonth = mn
				}
				break
			}
		}
		b.WriteString(theme.Muted.Render(lbl))
	}
	b.WriteString("\n")

	for r := 0; r < rows; r++ {
		b.WriteString(theme.Muted.Render(padStr(wd[r], 2)) + " ")
		for wi := range weeks {
			if !weeks[wi].has[r] {
				b.WriteString(lipgloss.NewStyle().Foreground(theme.Faint).Render(cellOff))
				continue
			}
			d := weeks[wi].cells[r]
			switch {
			case SameDay(d.Date, today):
				b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(theme.Accent).Render("▢ "))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(heatColors[heatLevel(d.Hours)]).Render(cellOn))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("  " + theme.Muted.Render("меньше ") +
		lipgloss.NewStyle().Foreground(heatColors[0]).Render(cellOn) +
		lipgloss.NewStyle().Foreground(heatColors[1]).Render(cellOn) +
		lipgloss.NewStyle().Foreground(heatColors[2]).Render(cellOn) +
		lipgloss.NewStyle().Foreground(heatColors[3]).Render(cellOn) +
		lipgloss.NewStyle().Foreground(heatColors[4]).Render(cellOn) +
		theme.Muted.Render(" больше"))
	return b.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func uiBar(frac float64, w int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(float64(w)*frac + 0.5)
	col := theme.Green
	switch {
	case frac < 0.34:
		col = theme.Red
	case frac < 0.7:
		col = theme.Amber
	}
	return lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(theme.GridEmpty).Render(strings.Repeat("░", w-filled)) +
		" " + theme.Text.Render(fmt.Sprintf("%.0f%%", frac*100))
}

func startOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func padStr(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func joinNL(v []string) string { return strings.Join(v, "\n") }

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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max1f(v float64) float64 {
	if v <= 0 {
		return 1
	}
	return v
}

func orZero(cond bool, zero, v int) int {
	if cond {
		return zero
	}
	return v
}

func orZeroF(cond bool, zero, v float64) float64 {
	if cond {
		return zero
	}
	return v
}

func profileIntensity(p *storage.Profile) float64 {
	if p == nil {
		return 1
	}
	return p.Intensity
}
