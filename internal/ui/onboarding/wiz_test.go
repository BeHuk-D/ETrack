package onboarding

import (
"strconv"
"testing"
"time"

tea "github.com/charmbracelet/bubbletea"
"etrack/internal/storage"
)

func newScoreStep(t *testing.T, scores []string) Model {
t.Helper()
db, err := storage.Open(":memory:")
if err != nil {
t.Fatal(err)
}
t.Cleanup(func() { db.Close() })
m := New(db, func() {})
m.step = stepSubjects
m.modeIdx = 1
m.addSubject("Информатика")
m.addSubject("Математика профильная")
m.addSubject("Русский язык")
m.step = stepPerSubjectScores
m.rowFocus = 0
m.syncRowInputs()
for i, v := range scores {
m.scoreInputs[i].SetValue(v)
}
m.focusRow()
return m
}

func TestEnterWhileFocusedAdvancesWizard(t *testing.T) {
m := newScoreStep(t, []string{"95", "90", "95"})
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
if nm.step != stepExamDates {
t.Fatalf("expected exam-dates step, got %d warn=%q", nm.step, nm.warning)
}
}

func TestEnterPreValidatesEmptyRows(t *testing.T) {
db, _ := storage.Open(":memory:")
defer db.Close()
m := New(db, func() {})
m.subjects = []draftSubject{
{name: "Русский язык", diff: 1.1, examDate: time.Now().AddDate(0, 8, 0)},
{name: "Физика", diff: 1.8, examDate: time.Now().AddDate(0, 8, 0)},
}
m.step = stepPerSubjectScores
m.syncRowInputs()
m.focusRow()
// empty fields must be pre-filled with suggested defaults
for i := range m.scoreInputs {
if m.scoreInputs[i].Value() == "" {
t.Fatalf("row %d not pre-filled", i)
}
v, err := parseOrZero(m.scoreInputs[i].Value())
if err != nil || v < 20 || v > 100 {
t.Fatalf("bad default %q row %d", m.scoreInputs[i].Value(), i)
}
}
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
if nm.step != stepExamDates {
t.Fatalf("expected advance with defaults, got %d warn=%q", nm.step, nm.warning)
}
}

func TestEnterRejectsInvalidScoreWithWarning(t *testing.T) {
m := newScoreStep(t, []string{"95", "150", "95"})
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
if nm.step == stepExamDates {
t.Fatal("should not advance with score 150")
}
if nm.warning == "" {
t.Fatal("expected validation warning")
}
}

func TestTabMovesBetweenRowsWhileTyping(t *testing.T) {
m := newScoreStep(t, []string{"95", "90", "95"})
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
if nm.rowFocus != 1 || !nm.scoreInputs[1].Focused() {
t.Fatalf("tab should move to row 1, focus=%d", nm.rowFocus)
}
nm, _ = nm.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
if nm.rowFocus != 0 || !nm.scoreInputs[0].Focused() {
t.Fatalf("shift+tab should return to row 0, focus=%d", nm.rowFocus)
}
}

func TestArrowsCommitEditedValues(t *testing.T) {
m := newScoreStep(t, []string{"95", "90", "95"})
m.scoreInputs[0].SetValue("88")
// Navigate down then back up; the final Enter must see the edited value.
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
nm, _ = nm.Update(tea.KeyMsg{Type: tea.KeyUp})
nm2, _ := nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
if nm2.step != stepExamDates || nm2.subjects[0].score != 88 {
t.Fatalf("edited value lost on navigation: score=%d step=%d warn=%q",
nm2.subjects[0].score, nm2.step, nm2.warning)
}
}

func TestEnterOnDatesStepAdvances(t *testing.T) {
m := newScoreStep(t, []string{"95", "90", "95"})
nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // -> dates
d := time.Now().AddDate(0, 8, 0).Format("2006-01-02")
for i := range nm.dateInputs {
nm.dateInputs[i].SetValue(d)
}
nm2, _ := nm.Update(tea.KeyMsg{Type: tea.KeyEnter})
if nm2.step != stepPreferences {
t.Fatalf("expected preferences step, got %d warn=%q", nm2.step, nm2.warning)
}
}

func parseOrZero(s string) (int, error) { return strconv.Atoi(s) }
