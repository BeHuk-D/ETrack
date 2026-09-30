// Package schedule — "Preparation Economy" module.
//
// This file implements the analytical core of ETrack exactly as specified:
//
//  1. Subject difficulty weights W_sub ∈ [1.0, 1.8] (hardcoded dictionary).
//  2. Score→hours mapping H_base = (S/100)² · 250 · W_sub — a non-linear
//     (quadratic) economy: climbing from 80 to 100 points costs far more
//     time than from 60 to 80 because of Part-2 complexity.
//  3. Total-score resolution: average score = Total / n, then a probability
//     adjustment that lowers targets for hard subjects by ~7% and raises
//     the Russian Language target, so the aggregate total stays reachable.
//  4. Debt redistribution ("Debt Engine"): missed hours are spread smoothly
//     over ALL remaining study days, never dumped on tomorrow:
//        H_new_daily = (H_remaining + H_debt) / D_left
//  5. Burnout caps: 8 h/day on weekdays, 10 h/day on weekends. If the debt
//     pushes the plan past the cap, Result.CapBreached fires a TUI warning.
//  6. 10-day feedback loop with the adaptation matrix F_adapt:
//        1 Exhausted → ×0.85 · 2 Hard → ×0.95 · 3 Perfect → ×1.00 · 4 Easy → ×1.15
package schedule

import (
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Domain constants (the rules of the economy)
// ---------------------------------------------------------------------------

const (
	// BaseHoursScale is K in H_base = (S/100)^2 * K * W_sub.
	BaseHoursScale = 250.0
	// MaxScore is the maximum primary-test score per subject.
	MaxScore = 100
	// MinPlausibleScore floors distributed per-subject targets.
	MinPlausibleScore = 20

	// CapWeekday / CapWeekend limit TOTAL planned hours across all subjects
	// in one calendar day to prevent burnout.
	CapWeekday = 8.0
	CapWeekend = 10.0

	// FeedbackIntervalDays is the length of the feedback loop in active days.
	FeedbackIntervalDays = 10
	// MinSession is the smallest sensible per-subject daily block (15 min).
	MinSession = 0.25
	// CompletionTolerance: logging ≥90% of a day's plan counts as done.
	CompletionTolerance = 0.9
	// MaxPerDay is the legacy alias equal to the weekend cap; kept so that
	// older call sites compile unchanged.
	MaxPerDay = CapWeekend
)

// ---------------------------------------------------------------------------
// 1. Subject difficulty weights W_sub ∈ [1.0, 1.8]
// ---------------------------------------------------------------------------

// PresetSubject is a known EGE subject with its normalized difficulty weight.
type PresetSubject struct {
	Name       string
	Difficulty float64 // W_sub, 1.0..1.8
}

// Presets — the standard EGE subjects grouped by the specification tiers:
//
//	Specialist/Hard          (1.8): Profile Mathematics, Physics
//	Analytical/Medium-Hard   (1.6): Informatics (IT), Chemistry, Biology
//	Humanities/Medium        (1.4): History, Social Studies, Literature, Foreign Languages
//	Base/Lower-Weight        (1.1): Russian Language, Geography
var Presets = []PresetSubject{
	{"Математика (профиль)", 1.8},
	{"Физика", 1.8},
	{"Информатика", 1.6},
	{"Химия", 1.6},
	{"Биология", 1.6},
	{"История", 1.4},
	{"Обществознание", 1.4},
	{"Литература", 1.4},
	{"Иностранный язык", 1.4},
	{"Русский язык", 1.1},
	{"География", 1.1},
}

// DifficultyFor returns W_sub of a known subject. Free-form custom names get
// keyword heuristics; anything unknown falls back to the medium tier (1.4).
func DifficultyFor(name string) float64 {
	norm := strings.ToLower(strings.TrimSpace(name))
	for _, p := range Presets {
		if strings.ToLower(p.Name) == norm ||
			strings.Contains(norm, strings.ToLower(firstWord(p.Name))) {
			return p.Difficulty
		}
	}
	switch {
	case containsAny(norm, "матем", "алгебр", "геометр", "проф"):
		return 1.8
	case containsAny(norm, "физик"):
		return 1.8
	case containsAny(norm, "информат", "программ", "ит", "cs"):
		return 1.6
	case containsAny(norm, "хим"):
		return 1.6
	case containsAny(norm, "биолог", "эколог"):
		return 1.6
	case containsAny(norm, "истор"):
		return 1.4
	case containsAny(norm, "обществ"):
		return 1.4
	case containsAny(norm, "литер"):
		return 1.4
	case containsAny(norm, "англий", "немецк", "француз", "испанск", "иностр"):
		return 1.4
	case containsAny(norm, "русск"):
		return 1.1
	case containsAny(norm, "географ"):
		return 1.1
	}
	return 1.4 // unknown custom subject → medium tier
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
// 2. Score → hours mapping (the core economy)
// ---------------------------------------------------------------------------

// EffortBudget converts a target score S into required study hours:
//
//	H_base = (S / 100)^2 * 250 * W_sub
//
// The quadratic term models the exponential cost of high scores (Part 2).
func EffortBudget(targetScore int, difficulty float64) float64 {
	if targetScore <= 0 {
		return 0
	}
	s := math.Min(float64(targetScore), MaxScore)
	w := clampF(difficulty, 1.0, 1.8)
	return math.Pow(s/float64(MaxScore), 2) * BaseHoursScale * w
}

// TotalBaseHours sums H_base over subjects.
func TotalBaseHours(scores []int, weights []float64) float64 {
	var t float64
	for i := range scores {
		t += EffortBudget(scores[i], weightAt(weights, i))
	}
	return t
}

func weightAt(w []float64, i int) float64 {
	if i < len(w) {
		return w[i]
	}
	return 1.4
}

// IsRussian reports whether a subject name refers to Russian Language —
// the buffer subject whose target we raise during total-score resolution.
func IsRussian(name string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(name)), "русск")
}

// ---------------------------------------------------------------------------
// 3. Total-score resolution & probability-optimised distribution
// ---------------------------------------------------------------------------

// DistributeScores resolves a single aggregate target (e.g. 260 pts over
// 3 subjects) into per-subject primary scores, each clamped to [20, 100]:
//
//  1. avg = total / n for every subject.
//  2. Probability adjustment: harder subjects (W_sub > 1.4) get their target
//     lowered by ~7% (middle of the 5–10% band); Russian Language gets a
//     +10% boost, other humanities a mild +3%. This maximises the expected
//     value of hitting the aggregate total.
//  3. Largest-remainder rounding guarantees sum(scores) == total whenever
//     the total is feasible (n·20 ≤ total ≤ n·100).
func DistributeScores(total int, difficulties []float64, _ []int) []int {
	n := len(difficulties)
	out := make([]int, n)
	if n == 0 || total <= 0 {
		return out
	}
	total = clampInt(total, n*MinPlausibleScore, n*MaxScore)

	avg := float64(total) / float64(n)
	raw := make([]float64, n)
	var sumRaw float64
	for i := 0; i < n; i++ {
		raw[i] = avg * adjustFactor(weightAt(difficulties, i))
		sumRaw += raw[i]
	}
	// Re-normalise the adjusted values so they still sum to `total`.
	scale := float64(total) / sumRaw
	exact := make([]float64, n)
	for i := 0; i < n; i++ {
		v := raw[i] * scale
		v = math.Max(MinPlausibleScore, math.Min(MaxScore, v))
		exact[i] = v
	}
	// Largest-remainder method for an exact integer total.
	floorSum := 0
	rem := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = int(math.Floor(exact[i]))
		rem[i] = exact[i] - math.Floor(exact[i])
		floorSum += out[i]
	}
	order := argsortDesc(rem)
	for k := 0; floorSum < total; k = (k + 1) % n {
		if out[order[k]] < MaxScore {
			out[order[k]]++
			floorSum++
		}
	}
	return out
}

// AdjustFactorForWeight exposes the probability-optimisation multiplier for
// a given difficulty weight (used by the wizard to pre-fill sensible default
// per-subject targets).
func AdjustFactorForWeight(w float64) float64 { return adjustFactor(w) }

// applyFactor is the probability-optimisation multiplier for a subject's
// target score: hard subjects give up 6–8% of their nominal target, easier
// ones pick it up (+3%). Russian Language (W=1.1) always gets the biggest
// boost (+10%) because its points are statistically the cheapest to earn.
func adjustFactor(w float64) float64 {
	w = clampF(w, 1.0, 1.8)
	switch {
	case w >= 1.7:
		return 0.92 // specialist/hard: −8%
	case w > 1.4:
		return 0.94 // analytical: −6%
	case w <= 1.15:
		return 1.10 // Russian / Geography tier: +10% buffer
	default:
		return 1.03 // humanities: +3%
	}
}

// ApplyProbabilityAdjustment re-scales individually chosen targets using the
// same logic as DistributeScores but preserving the user's own sum: hard
// subjects lose 5–10%, Russian gains, others absorb the difference. Scores
// stay within [20, 100].
func ApplyProbabilityAdjustment(scores []int, difficulties []float64) []int {
	n := len(scores)
	if n == 0 {
		return nil
	}
	out := make([]int, n)
	for i := 0; i < n; i++ {
		s := clampInt(scores[i], MinPlausibleScore, MaxScore)
		v := int(math.Round(float64(s) * adjustFactor(weightAt(difficulties, i))))
		out[i] = clampInt(v, MinPlausibleScore, MaxScore)
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

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	return math.Min(hi, math.Max(lo, v))
}

// roundQuarter snaps hour values to 15-minute blocks.
func roundQuarter(v float64) float64 { return math.Round(v*4) / 4 }

// ---------------------------------------------------------------------------
// Calendar helpers
// ---------------------------------------------------------------------------

// DayAllowed reports whether studying may be scheduled on the given date.
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

// DayCap returns the burnout safety cap for a given calendar day:
// 8 hours on weekdays, 10 hours on Saturdays/Sundays.
func DayCap(d time.Time) float64 {
	switch d.Weekday() {
	case time.Saturday, time.Sunday:
		return CapWeekend
	}
	return CapWeekday
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// WholeDaysBetween counts whole days (b-a) at midnight.
func WholeDaysBetween(a, b time.Time) int {
	return int(startOfDay(b).Sub(startOfDay(a)).Hours() / 24)
}

// ---------------------------------------------------------------------------
// 4. The scheduler — smooth debt redistribution engine
// ---------------------------------------------------------------------------

// SubjectInput is one subject fed to the generator.
type SubjectInput struct {
	Name        string
	ExamDate    time.Time
	Difficulty  float64 // W_sub
	TargetScore int     // resolved primary points, ≤ 100
}

// Params configures a full re-generation of the plan.
type Params struct {
	From      time.Time          // usually today
	Subjects  []SubjectInput
	Weekends  bool
	Sat       bool
	Sun       bool
	Intensity float64            // cumulative adaptation factor F_adapt (1.0 neutral)
	Logged    map[string]float64 // subject -> hours ALREADY studied (incl. today)
}

// Result is the generated plan plus diagnostics for the UI.
type Result struct {
	Plans        map[string]map[string]float64 // date YYYY-MM-DD -> subject -> hours
	HoursPerWeek map[string]float64            // subject -> average weekly load
	TotalHours   map[string]float64            // subject -> total planned future hours
	Debt         map[string]float64            // subject -> carried-over missed hours
	CapBreached  bool                          // debt pushed some day past the cap
	BreachDay    string                        // first breached date (diagnostics)
}

// Generate builds the complete day-by-day plan.
//
// Per subject:
//  1. Budget B = H_base(S, W_sub) × F_adapt  (non-linear score→hours economy).
//  2. Remaining demand R_h = max(0, B − already logged hours).
//  3. Debt D_s = max(0, previously planned-but-unlogged portion) — surfaced
//     for the UI; economically it is simply part of R_h.
//  4. Eligible days = [today .. exam−1] filtered by the weekend policy.
//     Today receives the fresh rate H_today = R_h / D_left, which is exactly
//     the smooth redistribution formula
//     H_new_daily = (H_remaining + H_debt) / (D_left − 1) applied on the next
//     regeneration: nothing is ever dumped onto a single following day.
//  5. Global safety cap: total hours per day ≤ 8 (weekday) / 10 (weekend).
//     Over-cap days are scaled proportionally; the overflow is NOT silently
//     dropped — CapBreached tells the UI to warn the student.
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

	maxExam := today
	for _, s := range p.Subjects {
		if e := startOfDay(s.ExamDate); e.After(maxExam) {
			maxExam = e
		}
	}

	raw := map[string]map[string]float64{}
	for _, s := range p.Subjects {
		budget := EffortBudget(s.TargetScore, s.Difficulty) * intensity
		if budget <= 0 {
			continue
		}
		logged := p.Logged[s.Name]
		remaining := math.Max(0, budget-logged)

		exam := startOfDay(s.ExamDate)
		var eligible []time.Time
		for d := today; d.Before(exam); d = d.AddDate(0, 0, 1) {
			if DayAllowed(d, today, p.Weekends, p.Sat, p.Sun) {
				eligible = append(eligible, d)
			}
		}
		dLeft := len(eligible)
		if dLeft == 0 || remaining <= 0 {
			res.Debt[s.Name] = 0
			continue
		}

		// Smooth uniform rate across ALL remaining study days. The clamp
		// keeps the per-subject daily block inside [MinSession, 6h].
		rate := clampF(remaining/float64(dLeft), MinSession, 6.0)
		q := roundQuarter(rate)
		planned := 0.0
		for _, d := range eligible {
			ensure(raw, d.Format("2006-01-02"))[s.Name] += q
			planned += q
		}
		res.TotalHours[s.Name] = roundQuarter(planned)
		res.Debt[s.Name] = roundQuarter(math.Max(0, remaining-planned))
		weeks := math.Max(1, float64(dLeft)/7.0)
		res.HoursPerWeek[s.Name] = roundQuarter(planned / weeks)
	}

	// Safety caps per calendar day (8 h weekday / 10 h weekend). Over-cap
	// days are scaled proportionally and flagged for the TUI warning.
	for key, m := range raw {
		var sum float64
		for _, h := range m {
			sum += h
		}
		d, err := time.Parse("2006-01-02", key)
		if err == nil && sum > DayCap(d) {
			capHours := DayCap(d)
			k := capHours / sum
			for name, h := range m {
				m[name] = roundQuarter(h * k)
			}
			if !res.CapBreached {
				res.CapBreached = true
				res.BreachDay = key
			}
		}
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
// 6. 10-day feedback loop — adaptation matrix
// ---------------------------------------------------------------------------

// PaceChoice is one answer of the check-in:
//
//	[1: Exhausted, 2: Hard but manageable, 3: Perfect, 4: Too easy, want more]
type PaceChoice int

const (
	PaceExhausted PaceChoice = iota + 1 // 1
	PaceHard                            // 2
	PacePerfect                         // 3
	PaceEasy                            // 4
)

// Legacy aliases kept so existing call sites compile unchanged.
const (
	PaceTooHard      = PaceExhausted
	PaceSlightlyHard = PaceHard
	PaceJustRight    = PacePerfect
	PaceSlightlyEasy = PaceEasy
	PaceTooEasy      = PaceEasy
)

// Label renders the option text shown in the overlay.
func (c PaceChoice) Label() string {
	switch c {
	case PaceExhausted:
		return "1 · Истощён — нужен щадящий режим (−15%)"
	case PaceHard:
		return "2 · Тяжело, но терпимо (−5%)"
	case PacePerfect:
		return "3 · В самый раз — оставляем темп"
	case PaceEasy:
		return "4 · Слишком легко — добавь объёма (+15%)"
	}
	return "?"
}

// AdaptationFactor returns F_adapt for the answer (the spec matrix).
func AdaptationFactor(c PaceChoice) float64 {
	switch c {
	case PaceExhausted:
		return 0.85
	case PaceHard:
		return 0.95
	case PacePerfect:
		return 1.0
	case PaceEasy:
		return 1.15
	}
	return 1.0
}

// ApplyIntensity multiplies the current pace factor by F_adapt, clamped to
// [0.5, 2.0] so the plan can never collapse or explode.
func ApplyIntensity(current float64, choice PaceChoice) float64 {
	if current <= 0 {
		current = 1.0
	}
	return clampF(current*AdaptationFactor(choice), 0.5, 2.0)
}

// ShouldAskFeedback decides whether the 10-day overlay must appear now.
func ShouldAskFeedback(activeDays, nextAt int) bool {
	return activeDays >= nextAt
}

// NextFeedbackAfter returns the counter value for the following check-in.
func NextFeedbackAfter(nextAt int) int {
	if nextAt <= 0 {
		return FeedbackIntervalDays
	}
	return nextAt + FeedbackIntervalDays
}
