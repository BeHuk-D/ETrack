package schedule

import (
"testing"
"time"
)

func TestDbg6(t *testing.T) {
today := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
s := SubjectInput{Name: "Химия", ExamDate: today.AddDate(0, 0, 31), Difficulty: 1.6, TargetScore: 75}
budget := EffortBudget(s.TargetScore, s.Difficulty) * 1.0
remaining := budget - 0.0
dLeft := 0
for d := today; d.Before(startOfDay(s.ExamDate)); d = d.AddDate(0, 0, 1) {
if DayAllowed(d, today, true, true, true) {
dLeft++
}
}
rate := clampF(remaining/float64(dLeft), MinSession, 6.0)
q := roundQuarter(rate)
t.Logf("budget=%v remaining=%v dLeft=%v raw rate=%v q=%v", budget, remaining, dLeft, remaining/float64(dLeft), q)
}
