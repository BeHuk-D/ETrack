// Package storage contains the persistence layer for ETrack.
// It is a thin, typed wrapper around SQLite (modernc.org/sqlite — pure Go,
// no cgo required) and knows nothing about the UI.
package storage

import "time"

// StudySlot is the part of the day a student prefers to study in.
type StudySlot string

const (
	SlotMorning   StudySlot = "morning"
	SlotAfternoon StudySlot = "afternoon"
	SlotEvening   StudySlot = "evening"
)

// AllStudySlots is the canonical ordering used by pickers in the UI.
var AllStudySlots = []StudySlot{SlotMorning, SlotAfternoon, SlotEvening}

func (s StudySlot) Label() string {
	switch s {
	case SlotMorning:
		return "Morning  · 06–12"
	case SlotAfternoon:
		return "Afternoon · 12–18"
	case SlotEvening:
		return "Evening  · 18–24"
	}
	return string(s)
}

// TargetMode decides how score targets are entered during onboarding.
type TargetMode string

const (
	TargetTotal      TargetMode = "total"      // one total score, distributed automatically
	TargetIndividual TargetMode = "individual" // per-subject targets, entered manually
)

// Profile is the single-row app state: user preferences + wizard progress.
type Profile struct {
	ID               int64
	TargetMode       TargetMode
	TotalTargetScore int // meaningful when TargetMode == total
	StudySlot        StudySlot
	StudyWeekends    bool    // master weekend flag
	SatAvailable     bool    // study allowed on Saturdays
	SunAvailable     bool    // study allowed on Sundays
	Onboarded        bool    // wizard completed
	ActiveDays       int     // number of days with at least one logged activity
	NextFeedbackDay  int     // active-day counter at which the feedback loop fires
	Intensity        float64 // scheduling multiplier (1.0 = neutral pace)
	CreatedAt        time.Time
}

// Subject is an EGE subject with its exam date and weighting.
type Subject struct {
	ID           int64
	Name         string
	ExamDate     time.Time // stored as YYYY-MM-DD
	Difficulty   float64   // relative weight used by the scheduler
	TargetScore  int       // target primary-test points for this subject
	HoursPerWeek float64   // last computed weekly load (display only)
	SortOrder    int
}

// DayPlan is one generated calendar day with per-subject hour budgets.
type DayPlan struct {
	Date    time.Time
	Subject map[string]float64 // subject name -> planned hours
}

// Total returns the planned hours for the day.
func (p DayPlan) Total() float64 {
	var t float64
	for _, h := range p.Subject {
		t += h
	}
	return t
}

// Entry is one row of the activity log: what was actually studied.
type Entry struct {
	ID          int64
	Date        time.Time
	SubjectName string
	Hours       float64
	Completed   bool
	Note        string
	CreatedAt   time.Time
}

// Feedback is one answer from the 10-day feedback loop.
type Feedback struct {
	ID        int64
	Date      time.Time
	Pace      string // "hard" | "ok" | "easy"
	Comment   string
	Intensity float64 // resulting multiplier after this answer
}
