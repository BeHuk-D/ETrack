package schedule

import (
	"math"
	"testing"
	"time"
)

func approx(t *testing.T, got, want float64, eps float64, what string) {
	t.Helper()
	if math.Abs(got-want) > eps {
		t.Errorf("%s: got %v, want ~%v", what, got, want)
	}
}

// --- 1. Difficulty weights -------------------------------------------------

func TestDifficultyWeightsInRange(t *testing.T) {
	for _, p := range Presets {
		if p.Difficulty < 1.0 || p.Difficulty > 1.8 {
			t.Errorf("W_sub(%s)=%v out of [1.0,1.8]", p.Name, p.Difficulty)
		}
	}
	if got := DifficultyFor("Математика (профиль)"); got != 1.8 {
		t.Errorf("math weight = %v, want 1.8", got)
	}
	if got := DifficultyFor("Русский язык"); got != 1.1 {
		t.Errorf("russian weight = %v, want 1.1", got)
	}
	if got := DifficultyFor("Информатика"); got != 1.6 {
		t.Errorf("informatics weight = %v, want 1.6", got)
	}
}

// --- 2. Score → hours mapping ----------------------------------------------

func TestEffortBudgetQuadratic(t *testing.T) {
	// H_base = (S/100)^2 * 250 * W
	approx(t, EffortBudget(100, 1.0), 250.0, 1e-9, "100 pts W=1")
	approx(t, EffortBudget(80, 1.0), 160.0, 1e-9, "80 pts W=1")
	approx(t, EffortBudget(50, 1.8), 112.5, 1e-9, "50 pts W=1.8")
	// Non-linearity: last 20 points cost more than the middle 20.
	dHigh := EffortBudget(100, 1.4) - EffortBudget(80, 1.4)
	dMid := EffortBudget(80, 1.4) - EffortBudget(60, 1.4)
	if dHigh <= dMid {
		t.Errorf("expected exponential cost curve: dHigh=%v dMid=%v", dHigh, dMid)
	}
	if EffortBudget(0, 1.5) != 0 {
		t.Error("zero score must map to zero hours")
	}
}

// --- 3. Total-score resolution ---------------------------------------------

func TestDistributeScoresCapsAndSum(t *testing.T) {
	diff := []float64{1.6, 1.8, 1.1} // Инф, Матм, Русский
	scores := DistributeScores(280, diff, nil)
	sum := 0
	for i, s := range scores {
		if s < MinPlausibleScore || s > MaxScore {
			t.Fatalf("subject %d got %d points — outside [%d,%d]", i, s, MinPlausibleScore, MaxScore)
		}
		sum += s
	}
	if sum != 280 {
		t.Errorf("sum=%d, want exactly 280", sum)
	}
	// Hard subjects lowered, Russian raised relative to the 93.3 average.
	if !(scores[1] < scores[2] && scores[0] < scores[2]) {
		t.Errorf("expected Russian boosted above hard subjects: %v", scores)
	}
}

func TestDistributeScoresExtremeTotalClamps(t *testing.T) {
	// 3×100 is the ceiling; asking for 320 must clamp, never exceed 100.
	scores := DistributeScores(320, []float64{1.8, 1.8, 1.8}, nil)
	for _, s := range scores {
		if s > MaxScore {
			t.Fatalf("score %d exceeds 100 cap", s)
		}
	}
}

func TestDistributeScoresNoHugeGaps(t *testing.T) {
	// The old sqrt(days) weighting produced 59 vs 118 — a spread of 59 pts.
	// The economy keeps the spread within ±20 pts of the average.
	scores := DistributeScores(280, []float64{1.6, 1.8, 1.1}, []int{200, 200, 200})
	avg := 280.0 / 3
	for _, s := range scores {
		if math.Abs(float64(s)-avg) > 20 {
			t.Errorf("score %d deviates from avg %.1f by more than 20 pts: %v", s, avg, scores)
		}
	}
}

// --- 4. Debt engine: smooth redistribution ---------------------------------

func TestDebtRedistributedSmoothlyNotOnTomorrow(t *testing.T) {
	today := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	exam := today.AddDate(0, 0, 11) // 10 study days left after regen day 0
	subj := SubjectInput{Name: "Физика", ExamDate: exam, Difficulty: 1.8, TargetScore: 80}

	// Day 0 plan with no logged hours yet.
	p0 := Params{From: today, Subjects: []SubjectInput{subj}, Weekends: true, Sat: true, Sun: true, Intensity: 1.0}
	r0 := Generate(p0)
	d0 := r0.Plans[today.Format("2006-01-02")]["Физика"]
	if d0 <= 0 {
		t.Fatal("no plan for day 0")
	}

	// User studies only half of the day-0 target → debt = d0/2.
	half := d0 / 2
	p1 := p0
	p1.From = today.AddDate(0, 0, 1)
	p1.Logged = map[string]float64{"Физика": half}
	r1 := Generate(p1)

	tomorrow := r1.Plans[p1.From.Format("2006-01-02")]["Физика"]
	dayAfter := r1.Plans[p1.From.AddDate(0, 0, 1).Format("2006-01-02")]["Физика"]

	// Smoothness: tomorrow's load ≈ day-after's load (uniform rate), and it
	// must NOT be the full debt dumped in one day (< 2× the original rate).
	if math.Abs(tomorrow-dayAfter) > 0.26 {
		t.Errorf("debt not spread smoothly: tomorrow=%v dayAfter=%v", tomorrow, dayAfter)
	}
	if tomorrow > 2*d0+0.01 {
		t.Errorf("debt dumped on next day: tomorrow=%v, original=%v", tomorrow, d0)
	}
	if tomorrow <= d0-1e-9 {
		t.Errorf("debt ignored: tomorrow=%v should exceed original %v", tomorrow, d0)
	}
}

func TestLoggedHoursReduceRemainingPlan(t *testing.T) {
	today := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// Moderate score over a long runway → rate (3.75 h/day) well above the
	// MinSession floor and below the 6h clamp, so logged hours must shrink
	// the remaining plan proportionally.
	subj := SubjectInput{Name: "Химия", ExamDate: today.AddDate(0, 0, 31), Difficulty: 1.6, TargetScore: 75}
	base := Generate(Params{From: today, Subjects: []SubjectInput{subj}, Weekends: true, Sat: true, Sun: true})
	withLog := Generate(Params{From: today, Subjects: []SubjectInput{subj},
		Weekends: true, Sat: true, Sun: true, Logged: map[string]float64{"Химия": 30}})
	if withLog.TotalHours["Химия"] >= base.TotalHours["Химия"] {
		t.Errorf("logged hours must shrink future plan: %v vs %v",
			withLog.TotalHours["Химия"], base.TotalHours["Химия"])
	}
}

func TestDailyCapWeekdayEightWeekendTen(t *testing.T) {
	// Monday start; four heavy subjects force over-capacity.
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) // Monday
	subs := []SubjectInput{
		{Name: "Математика (профиль)", ExamDate: mon.AddDate(0, 0, 8), Difficulty: 1.8, TargetScore: 95},
		{Name: "Физика", ExamDate: mon.AddDate(0, 0, 8), Difficulty: 1.8, TargetScore: 95},
		{Name: "Информатика", ExamDate: mon.AddDate(0, 0, 8), Difficulty: 1.6, TargetScore: 90},
		{Name: "Русский язык", ExamDate: mon.AddDate(0, 0, 8), Difficulty: 1.1, TargetScore: 90},
	}
	res := Generate(Params{From: mon, Subjects: subs, Weekends: true, Sat: true, Sun: true})
	for key, m := range res.Plans {
		d, _ := time.Parse("2006-01-02", key)
		var sum float64
		for _, h := range m {
			sum += h
		}
		if sum > DayCap(d)+0.51 { // rounding tolerance (quarter-hour snapping)
			t.Errorf("day %s planned %.2fh > cap %.1fh", key, sum, DayCap(d))
		}
	}
	if !res.CapBreached {
		t.Log("note: plan fit under caps without breach flag")
	}
}

func TestCapDirectValues(t *testing.T) {
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	sat := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	sun := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	if DayCap(mon) != CapWeekday || CapWeekday != 8.0 {
		t.Errorf("weekday cap = %v, want 8", DayCap(mon))
	}
	if DayCap(sat) != CapWeekend || DayCap(sun) != CapWeekend {
		t.Errorf("weekend cap = %v/%v, want 10", DayCap(sat), DayCap(sun))
	}
}

// --- 5. Feedback loop adaptation matrix ------------------------------------

func TestAdaptationMatrix(t *testing.T) {
	cases := []struct {
		c    PaceChoice
		want float64
	}{
		{PaceExhausted, 0.85},
		{PaceHard, 0.95},
		{PacePerfect, 1.0},
		{PaceEasy, 1.15},
	}
	for _, tc := range cases {
		if got := AdaptationFactor(tc.c); got != tc.want {
			t.Errorf("F_adapt(%d)=%v want %v", tc.c, got, tc.want)
		}
	}
}

func TestApplyIntensityMultiplicativeAndClamped(t *testing.T) {
	if got := ApplyIntensity(1.0, PaceExhausted); got != 0.85 {
		t.Errorf("exhausted from neutral: %v want 0.85", got)
	}
	if got := ApplyIntensity(0.6, PaceExhausted); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("lower clamp: %v want 0.5", got)
	}
	if got := ApplyIntensity(1.9, PaceEasy); got != 2.0 {
		t.Errorf("upper clamp: %v want 2.0", got)
	}
	// Perfect pace preserves trajectory.
	if got := ApplyIntensity(1.23, PacePerfect); got != 1.23 {
		t.Errorf("perfect must be ×1.0: %v", got)
	}
}

func TestFeedbackSchedule(t *testing.T) {
	if !ShouldAskFeedback(10, 10) {
		t.Error("must fire at day 10")
	}
	if ShouldAskFeedback(9, 10) {
		t.Error("must not fire early")
	}
	if NextFeedbackAfter(10) != 20 {
		t.Error("next check-in must be +10 active days")
	}
}

// --- Intensity actually changes generated volume ----------------------------

func TestIntensityScalesBudget(t *testing.T) {
	today := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// Long runway keeps the daily rate in the linear region of the engine
	// (above MinSession floor, below the 6h clamp), so F_adapt is visible.
	subj := SubjectInput{Name: "Биология", ExamDate: today.AddDate(0, 0, 31), Difficulty: 1.6, TargetScore: 75}
	neutral := Generate(Params{From: today, Subjects: []SubjectInput{subj}, Weekends: true, Sat: true, Sun: true, Intensity: 1.0})
	easy := Generate(Params{From: today, Subjects: []SubjectInput{subj}, Weekends: true, Sat: true, Sun: true, Intensity: 1.15})
	if easy.TotalHours["Биология"] <= neutral.TotalHours["Биология"] {
		t.Errorf("F_adapt=1.15 must raise planned hours: %v vs %v",
			easy.TotalHours["Биология"], neutral.TotalHours["Биология"])
	}
}
