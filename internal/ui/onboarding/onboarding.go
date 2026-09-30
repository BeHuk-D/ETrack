// Package onboarding implements the ETrack setup wizard as a self-contained
// Bubble Tea sub-model. The root model learns about completion only through
// the DoneFunc callback, keeping screens loosely coupled.
package onboarding

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"etrack/internal/schedule"
	"etrack/internal/storage"
	"etrack/internal/ui/theme"
)

// DoneFunc is invoked by the root model after the wizard persisted state.
type DoneFunc func()

type step int

const (
	stepWelcome step = iota
	stepTargetMode
	stepSubjects
	stepTotalScore
	stepPerSubjectScores
	stepExamDates
	stepPreferences
	stepReview
)

var stepTitles = map[step]string{
	stepWelcome:          "Добро пожаловать в ETrack",
	stepTargetMode:       "Шаг 1 · Как задаём цель по баллам?",
	stepSubjects:         "Шаг 2 · Предметы ЕГЭ",
	stepTotalScore:       "Шаг 2a · Суммарный целевой балл",
	stepPerSubjectScores: "Шаг 2b · Балл по каждому предмету",
	stepExamDates:        "Шаг 3 · Даты экзаменов",
	stepPreferences:      "Шаг 4 · Удобное время учёбы",
	stepReview:           "Проверь и запускай",
}

// Wizard steps shown in the progress rail (logical grouping).
var stepRail = []struct {
	label string
	steps []step
}{
	{"Цель", []step{stepTargetMode}},
	{"Предметы", []step{stepSubjects, stepTotalScore, stepPerSubjectScores}},
	{"Календарь", []step{stepExamDates}},
	{"Привычки", []step{stepPreferences}},
}

func maxSubjects() int { return 6 }

// draftSubject accumulates wizard input before anything hits the DB.
type draftSubject struct {
	name     string
	examDate time.Time
	score    int
	diff     float64
}

// Model is the wizard sub-model.
type Model struct {
	db   *storage.DB
	done DoneFunc

	step    step
	width   int
	height  int
	warning string
	err     string
	busy    bool

	// Step 1 — target mode radio.
	modeIdx int // 0 = total score, 1 = individual scores

	// Step 2 — subject picker.
	presets  []schedule.PresetSubject
	cursor   int
	inCustom bool
	custom   textinput.Model
	subjects []draftSubject
	rowFocus int
	totalVal int
	totalIn  textinput.Model

	// Steps 2b / 3 — per-row inputs.
	scoreInputs []textinput.Model
	dateInputs  []textinput.Model

	// Step 4 — preferences.
	slotIdx  int
	weekends bool
	sat      bool
	sun      bool
}

// New builds the wizard.
func New(db *storage.DB, done DoneFunc) Model {
	cust := textinput.New()
	cust.Placeholder = "например, Астрономия"
	cust.CharLimit = 48
	cust.Width = 30

	tot := textinput.New()
	tot.Placeholder = "220"
	tot.CharLimit = 3
	tot.Width = 10

	return Model{
		db:      db,
		done:    done,
		presets: schedule.Presets,
		custom:  cust,
		totalIn: tot,
	}
}

// SetSize propagates window geometry from tea.WindowSizeMsg.
func (m *Model) SetSize(w, h int) { m.width, m.height = w, h }

// Init implements the tea.Model contract for the sub-model.
func (m Model) Init() tea.Cmd { return nil }

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

type saveDoneMsg struct{ err error }

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if msg, ok := msg.(saveDoneMsg); ok {
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		if m.done != nil {
			m.done()
		}
		return m, nil
	}

	km, isKey := msg.(tea.KeyMsg)
	if isKey {
		m.err = ""
		m.warning = ""
		var cmd tea.Cmd
		m, cmd = m.handleKey(km, msg)
		return m, cmd
	}

	// Non-key messages (cursor blink ticks etc.) go to focused inputs.
	return m.passThrough(msg)
}

func (m Model) handleKey(km tea.KeyMsg, full tea.Msg) (Model, tea.Cmd) {
	key := km.String()

	// While typing into any text field, only that field consumes keys —
	// except Esc which blurs back to navigation mode.
	if m.typing() {
		if key == "esc" {
			m.blurAll()
			return m, nil
		}
		return m.passThrough(full)
	}

	switch m.step {
	case stepWelcome:
		m.step = stepTargetMode
		return m, nil

	case stepTargetMode:
		switch key {
		case "up", "down", "k", "j":
			m.modeIdx = (m.modeIdx + 1) % 2
		case "1":
			m.modeIdx = 0
		case "2":
			m.modeIdx = 1
		case "enter", "right", "l", "tab":
			m.step = stepSubjects
		case "left", "h":
			m.step = stepWelcome
		}
		return m, nil

	case stepSubjects:
		return m.updateSubjects(key, full)

	case stepTotalScore:
		switch key {
		case "enter", "tab":
			return m.tryAdvanceTotal()
		case "left", "h", "shift+tab":
			m.totalIn.Blur()
			m.step = stepSubjects
			return m, nil
		default:
			var cmd tea.Cmd
			m.totalIn, cmd = m.totalIn.Update(full)
			return m, cmd
		}

	case stepPerSubjectScores:
		switch key {
		case "up", "k":
			m.moveRow(-1)
			return m, nil
		case "down", "j", "tab":
			m.moveRow(+1)
			return m, nil
		case "shift+tab", "left", "h":
			m.blurAll()
			m.step = stepSubjects
			return m, nil
		case "enter":
			return m.tryAdvanceScores()
		default:
			return m.passThrough(full)
		}

	case stepExamDates:
		switch key {
		case "up", "k":
			m.moveRow(-1)
			return m, nil
		case "down", "j", "tab":
			m.moveRow(+1)
			return m, nil
		case "shift+tab", "left", "h":
			m.blurAll()
			if m.modeIdx == 0 {
				m.step = stepTotalScore
				m.totalIn.Focus()
			} else {
				m.step = stepPerSubjectScores
				m.focusRow()
			}
			return m, nil
		case "enter":
			return m.tryAdvanceDates()
		default:
			return m.passThrough(full)
		}

	case stepPreferences:
		switch key {
		case "up", "k", "left", "h":
			m.slotIdx = (m.slotIdx + 2) % 3
		case "down", "j", "right", "l":
			m.slotIdx = (m.slotIdx + 1) % 3
		case " ", "w":
			m.weekends = !m.weekends
			if m.weekends && !m.sat && !m.sun {
				m.sat, m.sun = true, true
			}
		case "s":
			m.sat = !m.sat
			if m.sat {
				m.weekends = true
			}
		case "u":
			m.sun = !m.sun
			if m.sun {
				m.weekends = true
			}
		case "enter", "tab":
			if m.weekends && !m.sat && !m.sun {
				m.warning = "Выходные включены, но не выбран ни один день (s — Сб, u — Вс)"
				return m, nil
			}
			m.step = stepReview
		case "shift+tab":
			m.step = stepExamDates
			m.focusRow()
		}
		return m, nil

	case stepReview:
		switch key {
		case "left", "h", "shift+tab":
			m.step = stepPreferences
		case "enter", "tab":
			if !m.busy {
				m.busy = true
				return m, m.persistCmd()
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateSubjects(key string, full tea.Msg) (Model, tea.Cmd) {
	if m.inCustom {
		if key == "esc" {
			m.inCustom = false
			m.custom.Blur()
			m.custom.SetValue("")
			return m, nil
		}
		var cmd tea.Cmd
		m.custom, cmd = m.custom.Update(full)
		if key == "enter" {
			val := strings.TrimSpace(m.custom.Value())
			if val == "" {
				m.warning = "Введи название предмета"
				return m, nil
			}
			m.addSubject(val)
			m.inCustom = false
			m.custom.SetValue("")
			m.custom.Blur()
		}
		return m, cmd
	}

	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.presets)-1 {
			m.cursor++
		}
	case " ":
		m.addSubject(m.presets[m.cursor].Name)
	case "x", "backspace", "delete":
		m.removeFocusedSubject()
	case "c":
		m.inCustom = true
		m.custom.Focus()
		return m, m.custom.Cursor.Activate()
	case "right", "l", "tab", "enter":
		if len(m.subjects) < 2 {
			m.warning = "Выбери минимум 2 предмета (пробел — добавить)"
			return m, nil
		}
		if m.modeIdx == 0 {
			m.step = stepTotalScore
			m.totalIn.Focus()
		} else {
			m.step = stepPerSubjectScores
			m.rowFocus = 0
			m.syncRowInputs()
		}
	case "left", "h", "shift+tab":
		m.step = stepTargetMode
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Subject list helpers
// ---------------------------------------------------------------------------

func (m *Model) addSubject(name string) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return
	}
	for _, s := range m.subjects {
		if strings.EqualFold(s.name, name) {
			m.warning = fmt.Sprintf("«%s» уже в списке", name)
			return
		}
	}
	if len(m.subjects) >= maxSubjects() {
		m.warning = fmt.Sprintf("Максимум %d предметов (x — удалить)", maxSubjects())
		return
	}
	m.subjects = append(m.subjects, draftSubject{
		name:     name,
		diff:     schedule.DifficultyFor(name),
		examDate: defaultExamDate(),
	})
	m.rowFocus = len(m.subjects) - 1
	m.syncRowInputs()
}

func (m *Model) removeFocusedSubject() {
	if len(m.subjects) == 0 {
		return
	}
	i := clampI(m.rowFocus, 0, len(m.subjects)-1)
	m.subjects = append(m.subjects[:i], m.subjects[i+1:]...)
	m.rowFocus = clampI(i, 0, max0(len(m.subjects)-1))
	m.syncRowInputs()
}

// ---------------------------------------------------------------------------
// Row editors
// ---------------------------------------------------------------------------

func (m *Model) syncRowInputs() {
	m.scoreInputs = make([]textinput.Model, len(m.subjects))
	m.dateInputs = make([]textinput.Model, len(m.subjects))
	for i, s := range m.subjects {
		si := textinput.New()
		si.Placeholder = "балл 20–100"
		si.CharLimit = 3
		si.Width = 10
		if s.score > 0 {
			si.SetValue(strconv.Itoa(s.score))
		}
		m.scoreInputs[i] = si

		di := textinput.New()
		di.Placeholder = "ГГГГ-ММ-ДД"
		di.CharLimit = 10
		di.Width = 12
		di.SetValue(s.examDate.Format("2006-01-02"))
		m.dateInputs[i] = di
	}
	m.rowFocus = clampI(m.rowFocus, 0, max0(len(m.subjects)-1))
	m.focusRow()
}

func (m *Model) moveRow(d int) {
	n := len(m.subjects)
	if n == 0 {
		return
	}
	m.rowFocus = (m.rowFocus + d + n) % n
	m.focusRow()
}

func (m *Model) focusRow() {
	for i := range m.scoreInputs {
		if m.step == stepPerSubjectScores && i == m.rowFocus {
			m.scoreInputs[i].Focus()
		} else {
			m.scoreInputs[i].Blur()
		}
	}
	for i := range m.dateInputs {
		if m.step == stepExamDates && i == m.rowFocus {
			m.dateInputs[i].Focus()
		} else {
			m.dateInputs[i].Blur()
		}
	}
}

func (m *Model) blurAll() {
	m.totalIn.Blur()
	m.custom.Blur()
	for i := range m.scoreInputs {
		m.scoreInputs[i].Blur()
	}
	for i := range m.dateInputs {
		m.dateInputs[i].Blur()
	}
}

func (m Model) typing() bool {
	if m.inCustom && m.custom.Focused() {
		return true
	}
	if m.step == stepTotalScore && m.totalIn.Focused() {
		return true
	}
	if m.step == stepPerSubjectScores && m.rowFocus < len(m.scoreInputs) && m.scoreInputs[m.rowFocus].Focused() {
		return true
	}
	if m.step == stepExamDates && m.rowFocus < len(m.dateInputs) && m.dateInputs[m.rowFocus].Focused() {
		return true
	}
	return false
}

// passThrough forwards a message to every focused text input.
func (m Model) passThrough(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	forward := func(ti *textinput.Model) {
		var c tea.Cmd
		*ti, c = ti.Update(msg)
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if m.custom.Focused() {
		forward(&m.custom)
	}
	if m.totalIn.Focused() {
		forward(&m.totalIn)
	}
	for i := range m.scoreInputs {
		if m.scoreInputs[i].Focused() {
			forward(&m.scoreInputs[i])
		}
	}
	for i := range m.dateInputs {
		if m.dateInputs[i].Focused() {
			forward(&m.dateInputs[i])
		}
	}
	return m, tea.Batch(cmds...)
}

// ---------------------------------------------------------------------------
// Validation & transitions
// ---------------------------------------------------------------------------

func (m Model) tryAdvanceTotal() (Model, tea.Cmd) {
	v := strings.TrimSpace(m.totalIn.Value())
	n, err := strconv.Atoi(v)
	if err != nil || n < 100 || n > 100*len(m.subjects) {
		m.warning = fmt.Sprintf("Суммарный балл: целое от 100 до %d", 100*len(m.subjects))
		return m, nil
	}
	m.totalVal = n
	m.blurAll()
	m.step = stepExamDates
	m.rowFocus = 0
	m.syncRowInputs()
	return m, nil
}

func (m Model) tryAdvanceScores() (Model, tea.Cmd) {
	for i := range m.subjects {
		v := strings.TrimSpace(m.scoreInputs[i].Value())
		n, err := strconv.Atoi(v)
		if err != nil || n < 20 || n > 100 {
			m.warning = fmt.Sprintf("«%s»: балл должен быть числом 20–100", m.subjects[i].name)
			return m, nil
		}
		m.subjects[i].score = n
	}
	m.blurAll()
	m.step = stepExamDates
	m.rowFocus = 0
	m.focusRow()
	return m, nil
}

func (m Model) tryAdvanceDates() (Model, tea.Cmd) {
	today := startOfToday()
	for i := range m.subjects {
		v := strings.TrimSpace(m.dateInputs[i].Value())
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			m.warning = fmt.Sprintf("«%s»: дата в формате ГГГГ-ММ-ДД (2027-06-07)", m.subjects[i].name)
			return m, nil
		}
		if d.Before(today.AddDate(0, 0, 3)) {
			m.warning = fmt.Sprintf("«%s»: до экзамена должно быть минимум 3 дня", m.subjects[i].name)
			return m, nil
		}
		m.subjects[i].examDate = d
	}
	m.blurAll()
	m.step = stepPreferences
	return m, nil
}

// persistCmd writes wizard results to SQLite inside a Cmd (no blocking I/O
// in Update). Schedule generation happens in the root model afterwards.
func (m Model) persistCmd() tea.Cmd {
	snapshot := m // copy drafts; the closure must not race with value semantics
	return func() tea.Msg {
		db := snapshot.db
		p, err := db.GetProfile()
		if err != nil {
			return saveDoneMsg{err}
		}
		mode := storage.TargetTotal
		if snapshot.modeIdx == 1 {
			mode = storage.TargetIndividual
		}
		subs := snapshot.subjects
		if mode == storage.TargetTotal {
			diffs := make([]float64, len(subs))
			days := make([]int, len(subs))
			now := startOfToday()
			for i, s := range subs {
				diffs[i] = s.diff
				days[i] = max1(schedule.WholeDaysBetween(now, s.examDate))
			}
			scores := schedule.DistributeScores(snapshot.totalVal, diffs, days)
			for i := range subs {
				subs[i].score = scores[i]
			}
		}
		// Idempotent re-onboarding: clear previous subjects first.
		old, _ := db.Subjects()
		for _, o := range old {
			_ = db.DeleteSubject(o.ID)
		}
		for i, s := range subs {
			if err := db.AddSubject(&storage.Subject{
				Name: s.name, ExamDate: s.examDate, Difficulty: s.diff,
				TargetScore: s.score, SortOrder: i,
			}); err != nil {
				return saveDoneMsg{fmt.Errorf("сохранить «%s»: %w", s.name, err)}
			}
		}
		p.TargetMode = mode
		if mode == storage.TargetTotal {
			p.TotalTargetScore = snapshot.totalVal
		} else {
			sum := 0
			for _, s := range subs {
				sum += s.score
			}
			p.TotalTargetScore = sum
		}
		p.StudySlot = storage.AllStudySlots[snapshot.slotIdx]
		p.StudyWeekends = snapshot.weekends
		p.SatAvailable = snapshot.sat
		p.SunAvailable = snapshot.sun
		p.Onboarded = true
		p.NextFeedbackDay = schedule.FeedbackIntervalDays
		if err := db.SaveProfile(p); err != nil {
			return saveDoneMsg{err}
		}
		return saveDoneMsg{}
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	innerW := clampI(m.width-10, 44, 92)

	head := lipgloss.JoinHorizontal(lipgloss.Center,
		theme.Brand.Render(" ETrack "),
		"  ",
		theme.Title.Render(stepTitles[m.step]),
	)

	var parts []string
	parts = append(parts, head, "", m.rail(), "", m.stepBody(innerW))
	if m.warning != "" {
		parts = append(parts, "", theme.Warn.Render("⚠ "+m.warning))
	}
	if m.err != "" {
		parts = append(parts, "", theme.Error.Render("✖ "+m.err))
	}
	if m.busy {
		parts = append(parts, "", theme.Subtitle.Render("сохраняю и строю план…"))
	}
	parts = append(parts, "", theme.Help.Render(m.helpForStep()))

	box := lipgloss.NewStyle().
		Width(innerW+4).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Render(joinNL(parts))

	return lipgloss.PlaceHorizontal(maxInt(m.width, innerW+8), lipgloss.Center, box)
}

func (m Model) rail() string {
	cur := m.railIndex()
	var parts []string
	for i, s := range stepRail {
		st := theme.StepTodo
		switch {
		case i == cur:
			st = theme.StepActive
		case i < cur:
			st = theme.StepDone
		}
		parts = append(parts, st.Render(fmt.Sprintf(" %d·%s ", i+1, s.label)))
	}
	if m.step == stepReview {
		parts = append(parts, theme.StepActive.Render(" ✓·Старт "))
	}
	return joinSp(parts)
}

func (m Model) railIndex() int {
	for i, s := range stepRail {
		for _, st := range s.steps {
			if st == m.step {
				return i
			}
		}
	}
	return len(stepRail)
}

func (m Model) stepBody(w int) string {
	switch m.step {
	case stepWelcome:
		return m.welcomeBody(w)
	case stepTargetMode:
		return m.targetModeBody()
	case stepSubjects:
		return m.subjectsBody()
	case stepTotalScore:
		return m.totalBody()
	case stepPerSubjectScores:
		return m.scoresBody()
	case stepExamDates:
		return m.datesBody()
	case stepPreferences:
		return m.prefsBody()
	case stepReview:
		return m.reviewBody()
	}
	return ""
}

func (m Model) welcomeBody(w int) string {
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(theme.Text).Render("Тренажёр подготовки к ЕГЭ"),
		"",
		theme.Muted.Render("За 4 коротких шага ETrack спросит цели по баллам, предметы,"),
		theme.Muted.Render("даты экзаменов и привычки — и построит умное расписание,"),
		theme.Muted.Render("которое само перераспределяет часы, если ты отстаёшь."),
		"",
		theme.Subtitle.Render("Нажми любую клавишу, чтобы начать →"),
	}
	return lipgloss.NewStyle().Width(w).Render(joinNL(lines))
}

func (m Model) targetModeBody() string {
	opts := []string{
		"Суммарный целевой балл — распределить автоматически",
		"Баллы по каждому предмету — задать вручную",
	}
	var lines []string
	for i, o := range opts {
		radio := "( )"
		st := theme.StepTodo
		if i == m.modeIdx {
			radio = "(●)"
			st = theme.StepActive
		}
		lines = append(lines, st.Render(radio+" "+o))
	}
	lines = append(lines, "", theme.Muted.Render("↑/↓ или 1/2 — выбор · enter — далее"))
	return joinNL(lines)
}

// presetItem is the view-model row of the preset checklist.
type presetItem struct {
	idx    int
	name   string
	diff   float64
	picked bool
}

func (m Model) subjectsBody() string {
	var lines []string
	lines = append(lines,
		theme.Muted.Render("пробел — добавить · x — удалить выделенный · c — свой предмет · enter — далее"), "")

	items := make([]presetItem, len(m.presets))
	for i, p := range m.presets {
		items[i] = presetItem{i, p.Name, p.Difficulty, m.hasSubject(p.Name)}
	}

	const colW = 36
	if m.width >= colW*2+16 {
		half := (len(items) + 1) / 2
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			presetCol(items[:half], m.cursor),
			presetCol(items[half:], m.cursor)))
	} else {
		lines = append(lines, presetCol(items, m.cursor))
	}

	lines = append(lines, "",
		theme.Text.Render(fmt.Sprintf("Выбрано (%d/%d):", len(m.subjects), maxSubjects())))
	if len(m.subjects) == 0 {
		lines = append(lines, theme.Faint.Render("  пока пусто — отмечай предметы слева пробелом"))
	}
	for i, s := range m.subjects {
		mark, st := "  ", theme.StepTodo
		if i == m.rowFocus {
			mark, st = "> ", theme.StepActive
		}
		lines = append(lines, st.Render(fmt.Sprintf("%s%-30s сложность ×%.2f", mark, s.name, s.diff)))
	}
	if m.inCustom {
		lines = append(lines, "", "Свой предмет: "+m.custom.View(),
			theme.Faint.Render("enter — добавить · esc — отмена"))
	}
	return joinNL(lines)
}

func presetCol(items []presetItem, cursor int) string {
	var b strings.Builder
	for _, it := range items {
		box := "[ ]"
		st := lipgloss.NewStyle().Foreground(theme.Text)
		if cursor == it.idx {
			box = "[•]"
			st = st.Bold(true).Foreground(theme.Accent)
		}
		if it.picked {
			box = "[✓]"
			st = st.Foreground(theme.Green)
			if cursor == it.idx {
				st = st.Bold(true)
			}
		}
		b.WriteString(st.Render(fmt.Sprintf("%s %-22s ×%.2f", box, trunc(it.name, 22), it.diff)) + "\n")
	}
	return b.String()
}

func (m Model) hasSubject(name string) bool {
	for _, s := range m.subjects {
		if strings.EqualFold(s.name, name) {
			return true
		}
	}
	return false
}

func (m Model) totalBody() string {
	var lines []string
	lines = append(lines,
		theme.Muted.Render("Сколько баллов суммарно нужно набрать?"),
		"",
		"  Суммарный балл: "+m.totalIn.View(),
		"",
	)
	diffs := make([]float64, len(m.subjects))
	days := make([]int, len(m.subjects))
	now := startOfToday()
	for i, s := range m.subjects {
		diffs[i] = s.diff
		days[i] = max1(schedule.WholeDaysBetween(now, s.examDate))
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.totalIn.Value())); err == nil && n >= 100 {
		scores := schedule.DistributeScores(n, diffs, days)
		lines = append(lines, theme.Muted.Render("Предварительное распределение (вес = сложность × √дней):"))
		for i, s := range m.subjects {
			lines = append(lines, theme.Text.Render(fmt.Sprintf("  %-28s %3d баллов", s.name, scores[i])))
		}
	} else {
		lines = append(lines, theme.Faint.Render("  введи число ≥ 100, чтобы увидеть распределение"))
	}
	lines = append(lines, "", theme.Help.Render("enter — далее · shift+tab — назад"))
	return joinNL(lines)
}

func (m Model) scoresBody() string {
	var lines []string
	lines = append(lines, theme.Muted.Render("Укажи целевой первичный балл по каждому предмету (20–100)."), "")
	for i, s := range m.subjects {
		mark, st := "  ", theme.StepTodo
		if i == m.rowFocus {
			mark, st = "> ", theme.StepActive
		}
		lines = append(lines, st.Render(fmt.Sprintf("%s%-28s %s", mark, s.name, m.scoreInputs[i].View())))
	}
	lines = append(lines, "", theme.Help.Render("↑/↓ — строки · enter — далее · shift+tab — назад"))
	return joinNL(lines)
}

func (m Model) datesBody() string {
	var lines []string
	lines = append(lines, theme.Muted.Render("Когда экзамены? Формат даты — ГГГГ-ММ-ДД (ЕГЭ обычно в июне)."), "")
	for i, s := range m.subjects {
		mark, st := "  ", theme.StepTodo
		if i == m.rowFocus {
			mark, st = "> ", theme.StepActive
		}
		lines = append(lines, st.Render(fmt.Sprintf("%s%-28s %s", mark, s.name, m.dateInputs[i].View())))
	}
	lines = append(lines, "", theme.Help.Render("↑/↓ — строки · enter — далее · shift+tab — назад"))
	return joinNL(lines)
}

func (m Model) prefsBody() string {
	var lines []string
	lines = append(lines, theme.Muted.Render("Когда учиться? В это окно планировщик ставит основные блоки."), "")
	for i, sl := range storage.AllStudySlots {
		mark, st := "( )", theme.StepTodo
		if i == m.slotIdx {
			mark, st = "(●)", theme.StepActive
		}
		lines = append(lines, st.Render(mark+" "+sl.Label()))
	}
	lines = append(lines, "", theme.Muted.Render("Выходные (w — общий переключатель, s — суббота, u — воскресенье):"))
	lines = append(lines, checkbox("Учиться по выходным", m.weekends),
		"   "+checkbox("Суббота", m.sat),
		"   "+checkbox("Воскресенье", m.sun))
	lines = append(lines, "", theme.Help.Render("←/→ — слот времени · enter — далее · h — назад"))
	return joinNL(lines)
}

func (m Model) reviewBody() string {
	mode := "Суммарный балл"
	if m.modeIdx == 1 {
		mode = "По предметам"
	}
	var lines []string
	lines = append(lines,
		theme.Muted.Render("Режим целей: ")+theme.Text.Render(mode),
		theme.Muted.Render("Слот времени: ")+theme.Text.Render(storage.AllStudySlots[m.slotIdx].Label()),
		theme.Muted.Render("Выходные: ")+theme.Text.Render(fmt.Sprintf("мастер=%v · сб=%v · вс=%v", m.weekends, m.sat, m.sun)),
		"",
		theme.Text.Render("Предметы:"),
	)
	for _, s := range m.subjects {
		scoreTxt := "авто"
		if s.score > 0 {
			scoreTxt = strconv.Itoa(s.score)
		}
		lines = append(lines, theme.Text.Render(fmt.Sprintf("  %-28s %5s б.  экзамен %s  ×%.2f",
			s.name, scoreTxt, s.examDate.Format("02.01.2006"), s.diff)))
	}
	lines = append(lines, "", theme.Success.Render("enter — сохранить и построить план"))
	return joinNL(lines)
}

func (m Model) helpForStep() string {
	switch m.step {
	case stepWelcome:
		return "любая клавиша — старт"
	case stepSubjects:
		return "↑/↓ список · пробел выбрать · x удалить · c свой · enter далее · ctrl+c выход"
	case stepTotalScore, stepPerSubjectScores, stepExamDates:
		return "ввод значений · enter далее · shift+tab назад"
	case stepPreferences:
		return "←/→ слот · w/s/u выходные · enter далее"
	case stepReview:
		return "enter подтвердить · h назад"
	}
	return "ctrl+c — выход"
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func checkbox(label string, on bool) string {
	box := "[ ]"
	st := theme.StepTodo
	if on {
		box = "[✓]"
		st = theme.StepDone
	}
	return st.Render(box + " " + label)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
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

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startOfToday() time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func defaultExamDate() time.Time {
	return startOfToday().AddDate(0, 8, 0) // ~8 months ahead: typical EGE in June
}

func joinNL(v []string) string { return strings.Join(v, "\n") }
func joinSp(v []string) string { return strings.Join(v, " ") }
