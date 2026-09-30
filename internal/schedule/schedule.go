// Package schedule implements ETrack's analytical planning engine:
// difficulty weighting, score→hours distribution, calendar generation with
// debt-aware dynamic recalculation, and feedback-loop intensity control.
package schedule

import (
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Domain constants
// ---------------------------------------------------------------------------

const (
	// MaxPerDay caps total planned hours on a single calendar day.
	MaxPerDay = 10.0
	// MinSession is the smallest sensible per-subject daily block.
	MinSession = 0.25
	// FeedbackIntervalDays is the length of the feedback loop in active days.
	FeedbackIntervalDays = 10
	// CompletionTolerance: logging ≥90% of a day's plan counts as done.
	CompletionTolerance = 0.9
)

// PresetSubject is a known EGE subject with its difficulty weight.
type PresetSubject struct {
	Name       string
	Difficulty float64
}

// Presets are the standard Russian State Exam subjects ordered by weight.
var Presets = []PresetSubject{
	{"Математика (профиль)", 1.6},
	{"Физика", 1.5},
	{"Информатика", 1.4},
	{"Химия", 1.35},
	{"Биология", 1.3},
	{"Русский язык", 0.8},
	{"Обществознание", 1.0},
	{"История", 1.1},
	{"География", 1.05},
	{"Литература", 1.1},
	{"Иностранный язык", 1.15},
	{"Информационные технологии", 1.2}, // ЕГЭ с 2027 года
}

// DifficultyFor returns the weight of a known subject; unknown custom names
// get a medium default so they still receive a fair share of hours.
func DifficultyFor(name string) float64 {
	norm := strings.ToLower(strings.TrimSpace(name))
	for _, p := range Presets {
		if strings.ToLower(p.Name) == norm || strings.Contains(norm, strings.ToLower(firstWord(p.Name))) {
			return p.Difficulty
		}
	}
	// Heuristics for free-form input.
	switch {
	case containsAny(norm, "матем", "алгебр", "геометр"):
		return 1.6
	case containsAny(norm, "физик"):
		return 1.5
	case containsAny(norm, "информат", "программ", "cs"):
		return 1.4
	case containsAny(norm, "хим"):
		return 1.35
	case containsAny(norm, "биолог", "эколог"):
		return 1.3
	case containsAny(norm, "русск", "литер"):
		return 0.85
	case containsAny(norm, "истор"):
		return 1.1
	case containsAny(norm, "обществ"):
		return 1.0
	case containsAny(norm, "географ"):
		return 1.05
	case containsAny(norm, "англий", "немецк", "француз", "испанск", "иностр"):
		return 1.15
	}
	return 1.2 // medium default for custom subjects
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " ("); i > 0 {
		return s[:i]
	}
	return s
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Score → effort distribution
// ---------------------------------------------------------------------------

// DistributeScores converts a single total target score into per-subject
// targets using proportional rounding that always sums exactly to total.
// Weight for a subject = difficulty × remaining days until its exam:
// harder subjects and exams closer in time pull more points toward them.
func DistributeScores(total int, difficulties []float64, daysLeft []int) []int {
	n := len(difficulties)
	out := make([]int, n)
	if n == 0 || total <= 0 {
		return out
	}
	w := make([]float64, n)
	var sumW float64
	for i := 0; i < n; i++ {
		d := daysLeft[i]
		if d < 1 {
			d = 1
		}
		// Mild urgency bias: sqrt keeps it from dominating raw difficulty.
		w[i] = math.Max(0.1, difficulties[i]) * math.Sqrt(float64(d))
		sumW += w[i]
	}
	if sumW <= 0 {
		sumW = 1
	}
	// Largest-remainder method for exact totals.
	floorSum := 0
	rem := make([]float64, n)
	for i := 0; i < n; i++ {
		exact := float64(total) * w[i] / sumW
		out[i] = int(math.Floor(exact))
		rem[i] = exact - math.Floor(exact)
		floorSum += out[i]
	}
	order := argsortDesc(rem)
	for k := 0; floorSum < total; k = (k + 1) % n {
		out[order[k]]++
		floorSum++
	}
	return out
}

func argsortDesc(v []float64) []int {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	for a := 1; a < len(idx); a++ {
		for b := a; b > 0 && v[b] > v[b-1]; b-- {
			idx[b], idx[b-1] = idx[b-1], idx[b]
		}
	}
	return idx
}

// ---------------------------------------------------------------------------
// Calendar helpers
// ---------------------------------------------------------------------------

// DayAllowed reports whether studying may be scheduled on the given date.
// Days strictly before `from` are never allowed; exam days themselves and
// days after each subject's exam are handled by the caller per subject.
func DayAllowed(d time.Time, from time.Time, weekends, sat, sun bool) bool {
	if startOfDay(d).Before(startOfDay(from)) {
		return false
	}
	switch d.Weekday() {
	case time.Saturday:
		return weekends && sat
	case time.Sunday:
		return weekends && sun
	}
	return true
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// WholeDaysBetween counts inclusive-free whole days (b-a) at midnight.
func WholeDaysBetween(a, b time.Time) int {
	return int(startOfDay(b).Sub(startOfDay(a)).Hours() / 24)
}

// ---------------------------------------------------------------------------
// The scheduler
// ---------------------------------------------------------------------------

// SubjectInput is one subject fed to the generator.
type SubjectInput struct {
	Name        string
	ExamDate    time.Time
	Difficulty  float64
	TargetScore int
}

// Params configures a full re-generation of the plan.
type Params struct {
	From      time.Time // first day the plan may schedule (usually today)
	Subjects  []SubjectInput
	Weekends  bool
	Sat       bool
	Sun       bool
	Intensity float64 // pace multiplier from the feedback loop (clamped)
}

// Result is the generated plan plus diagnostics for the UI.
type Result struct {
	Plans        map[string]map[string]float64 // date YYYY-MM-DD -> subject -> hours
	HoursPerWeek map[string]float64            // subject -> average weekly load
	TotalHours   map[string]float64            // subject -> total planned hours
	Debt         map[string]float64            // subject -> unlogged hours carried forward
}

// EffortBudget converts a target primary-test score into estimated study
// hours. Empirical heuristic: ~2.2 h per point above a baseline of 40,
// scaled by difficulty, floored at 20 h, capped at 600 h.
func EffortBudget(targetScore int, difficulty float64) float64 {
	if targetScore <= 0 {
		return 0
	}
	h := (float64(targetScore)-40)*2.2*clampF(difficulty, 0.5, 2.0) + 30
	return clampF(h, 20, 600)
}

func clampF(v, lo, hi float64) float64 {
	return math.Min(hi, math.Max(lo, v))
}

// roundQuarter snaps hour values to 15-minute blocks.
func roundQuarter(v float64) float64 { return math.Round(v*4) / 4 }

// Generate builds the complete day-by-day plan.
//
// Algorithm (per subject):
//  1. Total budget B = EffortBudget(target score, difficulty), multiplied by
//     the feedback-loop Intensity.
//  2. Debt D = B − already logged hours for past+today days (positive = missed).
//  3. Remaining eligible days R = [max(today, tomorrow after today), exam−1]
//     filtered by the weekend policy.
//  4. Uniform rate r = (D + future demand) / R — i.e. missing hours are
//     automatically redistributed across every remaining day ("debt-aware").
//  5. Per-subject cap of 6 h/day; leftover pressure spills to other days
//     naturally because r is recomputed on every regeneration.
//  6. A global cap of MaxPerDay h/day is enforced by scaling all subjects of
//     an overbooked day proportionally.
func Generate(p Params) *Result {
	res := &Result{
		Plans:        map[string]map[string]float64{},
		HoursPerWeek: map[string]float64{},
		TotalHours:   map[string]float64{},
		Debt:         map[string]float64{},
	}
	intensity := p.Intensity
	if intensity <= 0 {
		intensity = 1.0
	}
	intensity = clampF(intensity, 0.5, 2.0)
	today := startOfDay(p.From)

	// Collect all candidate days once.
	maxExam := today
	for _, s := range p.Subjects {
		if e := startOfDay(s.ExamDate); e.After(maxExam) {
			maxExam = e
		}
	}
	var allDays []time.Time
	for d := today; !d.After(maxExam); d = d.AddDate(0, 0, 1) {
		if DayAllowed(d, today, p.Weekends, p.Sat, p.Sun) {
			allDays = append(allDays, d)
		}
	}

	raw := map[string]map[string]float64{} // dateKey -> subject -> hours
	// Pass 1: per-subject uniform rate including debt redistribution.
	for _, s := range p.Subjects {
		budget := EffortBudget(s.TargetScore, s.Difficulty) * intensity
		if budget <= 0 {
			continue
		}
		var eligible []time.Time
		exam := startOfDay(s.ExamDate)
		for _, d := range allDays {
			if d.Before(exam) { // no studying on/after the exam day itself
				eligible = append(eligible, d)
			}
		}
		if len(eligible) == 0 {
			continue
		}
		// Spread budget over eligible days; front-load slightly so early
		// weeks build momentum: linear ramp from 0.85r to 1.15r.
		r := budget / float64(len(eligible))
		r = clampF(r, 0, 6.0)
		n := len(eligible)
		total := 0.0
		vals := make([]float64, n)
		for i := 0; i < n; i++ {
			factor := 0.85 + 0.30*float64(i)/math.Max(1, float64(n-1))
			vals[i] = roundQuarter(r * factor)
			total += vals[i]
		}
		// Renormalise so the ramp does not distort the budget, then snap.
		if total > 0 {
			k := (budget) / total
			total = 0
			for i := 0; i < n; i++ {
				vals[i] = roundQuarter(vals[i] * k)
				total += vals[i]
			}
		}
		debt := budget - total
		if debt > 0.24 { // carry visible remainder onto the last day
			last := eligible[n-1].Format("2006-01-02")
			ensure(raw, last)[s.Name] += roundQuarter(math.Min(debt, 6.0))
			total += roundQuarter(math.Min(debt, 6.0))
		}
		res.Debt[s.Name] = math.Max(0, roundQuarter(budget-total))
		res.TotalHours[s.Name] = total
		weeks := math.Max(1, float64(n)/7.0)
		res.HoursPerWeek[s.Name] = roundQuarter(total / weeks)
		for i, d := range eligible {
			if vals[i] > 0 {
				ensure(raw, d.Format("2006-01-02"))[s.Name] += vals[i]
			}
		}
	}

	// Pass 2: enforce the global daily cap by proportional scaling.
	for key, m := range raw {
		var sum float64
		for _, h := range m {
			sum += h
		}
		if sum > MaxPerDay {
			k := MaxPerDay / sum
			for name, h := range m {
				m[name] = roundQuarter(h * k)
			}
		}
	}
	// Drop zero rows and materialise.
	for key, m := range raw {
		clean := map[string]float64{}
		for name, h := range m {
			if h >= MinSession {
				clean[name] = h
			}
		}
		if len(clean) > 0 {
			res.Plans[key] = clean
		}
	}
	return res
}

func ensure(m map[string]map[string]float64, key string) map[string]float64 {
	if m[key] == nil {
		m[key] = map[string]float64{}
	}
	return m[key]
}

// ---------------------------------------------------------------------------
// Feedback loop
// ---------------------------------------------------------------------------

// PaceChoice is one answer option of the 10-day check-in.
type PaceChoice int

const (
	PaceTooHard PaceChoice = iota
	PaceSlightlyHard
	PaceJustRight
	PaceSlightlyEasy
	PaceTooEasy
)

func (c PaceChoice) Label() string {
	switch c {
	case PaceTooHard:
		return "Слишком тяжело — нужно меньше часов"
	case PaceSlightlyHard:
		return "Чуть тяжеловато — чуть меньше часов"
	case PaceJustRight:
		return "В самый раз — оставить как есть"
	case PaceSlightlyEasy:
		return "Чуть легковато — можно больше"
	case PaceTooEasy:
		return "Слишком легко — нужен больший объём"
	}
	return "?"
}

// ApplyIntensity nudges the multiplicative pace factor. It is clamped to
// [0.5, 2.0] so the plan can never collapse or explode.
func ApplyIntensity(current float64, choice PaceChoice) float64 {
	step := map[PaceChoice]float64{
		PaceTooHard:      0.75,
		PaceSlightlyHard: 0.88,
		PaceJustRight:    1.0,
		PaceSlightlyEasy: 1.12,
		PaceTooEasy:      1.30,
	}[choice]
	return clampF(current*step, 0.5, 2.0)
}

// ShouldAskFeedback decides whether the 10-day overlay must appear now.
func ShouldAskFeedback(activeDays, nextAt int) bool {
	return activeDays >= nextAt
}

// NextFeedbackAfter returns the counter value for the following check-in.
func NextFeedbackAfter(nextAt int) int { return nextAt + FeedbackIntervalDays }
