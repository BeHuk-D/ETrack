package storage

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// Schema versioning is stored in pragma user_version.
const schemaVersion = 1

const schemaSQL = `
CREATE TABLE IF NOT EXISTS profile (
    id                INTEGER PRIMARY KEY CHECK (id = 1), -- singleton row
    target_mode       TEXT    NOT NULL DEFAULT 'total',
    total_target      INTEGER NOT NULL DEFAULT 0,
    study_slot        TEXT    NOT NULL DEFAULT 'evening',
    study_weekends    INTEGER NOT NULL DEFAULT 0,
    sat_available     INTEGER NOT NULL DEFAULT 0,
    sun_available     INTEGER NOT NULL DEFAULT 0,
    onboarded         INTEGER NOT NULL DEFAULT 0,
    active_days       INTEGER NOT NULL DEFAULT 0,
    next_feedback_day INTEGER NOT NULL DEFAULT 10,
    intensity         REAL    NOT NULL DEFAULT 1.0,
    created_at        TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS subject (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    exam_date    TEXT NOT NULL,               -- YYYY-MM-DD
    difficulty   REAL NOT NULL DEFAULT 1.0,   -- relative weight
    target_score INTEGER NOT NULL DEFAULT 0,  -- primary-test points
    hours_per_week REAL NOT NULL DEFAULT 0,   -- last scheduler output
    sort_order   INTEGER NOT NULL DEFAULT 0
);

-- Generated daily slots: planned hours per subject per day.
CREATE TABLE IF NOT EXISTS schedule (
    date         TEXT NOT NULL,               -- YYYY-MM-DD
    subject_name TEXT NOT NULL,
    target_hours REAL NOT NULL,
    PRIMARY KEY (date, subject_name)
);

-- Activity log: what the student actually studied.
CREATE TABLE IF NOT EXISTS activity_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    date         TEXT NOT NULL,               -- YYYY-MM-DD
    subject_name TEXT NOT NULL,
    hours        REAL NOT NULL,
    completed    INTEGER NOT NULL DEFAULT 0,
    note         TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_log_date ON activity_log (date);

-- Answers for the 10-day feedback loop.
CREATE TABLE IF NOT EXISTS feedback (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    date      TEXT NOT NULL,
    pace      TEXT NOT NULL,                  -- hard | ok | easy
    comment   TEXT NOT NULL DEFAULT '',
    intensity REAL NOT NULL
);
`

// DB wraps a *sql.DB with ETrack-specific queries.
type DB struct{ db *sql.DB }

// Open opens (and migrates) the SQLite database at path.
// Use ":memory:" for tests.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal(WAL)&_pragma=busy_timeout(4000)&_pragma=foreign_keys(1)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc/sqlite works best with a single writer connection.
	sqlDB.SetMaxOpenConns(1)
	d := &DB{db: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the underlying handle.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	var v int
	if err := d.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v >= schemaVersion {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	// Ensure the singleton profile row exists.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO profile (id) VALUES (1)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Profile
// ---------------------------------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetProfile loads the singleton profile row.
func (d *DB) GetProfile() (*Profile, error) {
	p := &Profile{}
	var mode, slot, created string
	var onboarded, weekends, sat, sun int
	err := d.db.QueryRow(`SELECT id, target_mode, total_target, study_slot,
		study_weekends, sat_available, sun_available, onboarded,
		active_days, next_feedback_day, intensity, created_at
		FROM profile WHERE id = 1`,
	).Scan(&p.ID, &mode, &p.TotalTargetScore, &slot, &weekends, &sat, &sun,
		&onboarded, &p.ActiveDays, &p.NextFeedbackDay, &p.Intensity, &created)
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	p.TargetMode = TargetMode(mode)
	p.StudySlot = StudySlot(slot)
	p.StudyWeekends = weekends == 1
	p.SatAvailable = sat == 1
	p.SunAvailable = sun == 1
	p.Onboarded = onboarded == 1
	if t, err := time.Parse("2006-01-02 15:04:05", created); err == nil {
		p.CreatedAt = t
	}
	return p, nil
}

// SaveProfile persists mutable profile fields.
func (d *DB) SaveProfile(p *Profile) error {
	_, err := d.db.Exec(`UPDATE profile SET target_mode=?, total_target=?, study_slot=?,
		study_weekends=?, sat_available=?, sun_available=?, onboarded=?,
		active_days=?, next_feedback_day=?, intensity=? WHERE id=1`,
		string(p.TargetMode), p.TotalTargetScore, string(p.StudySlot),
		boolToInt(p.StudyWeekends), boolToInt(p.SatAvailable), boolToInt(p.SunAvailable),
		boolToInt(p.Onboarded), p.ActiveDays, p.NextFeedbackDay, p.Intensity)
	return err
}

// ---------------------------------------------------------------------------
// Subjects
// ---------------------------------------------------------------------------

// AddSubject inserts a subject; names must be unique.
func (d *DB) AddSubject(s *Subject) error {
	res, err := d.db.Exec(`INSERT INTO subject (name, exam_date, difficulty, target_score, hours_per_week, sort_order)
		VALUES (?,?,?,?,?,?)`,
		s.Name, s.ExamDate.Format("2006-01-02"), s.Difficulty, s.TargetScore, s.HoursPerWeek, s.SortOrder)
	if err != nil {
		return err
	}
	s.ID, _ = res.LastInsertId()
	return nil
}

// UpdateSubject overwrites an existing subject row.
func (d *DB) UpdateSubject(s *Subject) error {
	_, err := d.db.Exec(`UPDATE subject SET name=?, exam_date=?, difficulty=?,
		target_score=?, hours_per_week=?, sort_order=? WHERE id=?`,
		s.Name, s.ExamDate.Format("2006-01-02"), s.Difficulty, s.TargetScore,
		s.HoursPerWeek, s.SortOrder, s.ID)
	return err
}

// DeleteSubject removes a subject together with its schedule & log rows.
func (d *DB) DeleteSubject(id int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRow(`SELECT name FROM subject WHERE id=?`, id).Scan(&name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM schedule WHERE subject_name=?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM activity_log WHERE subject_name=?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subject WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Subjects returns all subjects ordered by exam date.
func (d *DB) Subjects() ([]Subject, error) {
	rows, err := d.db.Query(`SELECT id, name, exam_date, difficulty, target_score,
		hours_per_week, sort_order FROM subject ORDER BY exam_date, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subject
	for rows.Next() {
		var s Subject
		var date string
		if err := rows.Scan(&s.ID, &s.Name, &date, &s.Difficulty, &s.TargetScore,
			&s.HoursPerWeek, &s.SortOrder); err != nil {
			return nil, err
		}
		s.ExamDate, _ = time.Parse("2006-01-02", date)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SubjectByName looks up a single subject.
func (d *DB) SubjectByName(name string) (*Subject, error) {
	var s Subject
	var date string
	err := d.db.QueryRow(`SELECT id, name, exam_date, difficulty, target_score,
		hours_per_week, sort_order FROM subject WHERE name=?`, name).
		Scan(&s.ID, &s.Name, &date, &s.Difficulty, &s.TargetScore, &s.HoursPerWeek, &s.SortOrder)
	if err != nil {
		return nil, err
	}
	s.ExamDate, _ = time.Parse("2006-01-02", date)
	return &s, nil
}

// ---------------------------------------------------------------------------
// Schedule
// ---------------------------------------------------------------------------

// ReplaceSchedule atomically swaps in a freshly generated plan.
func (d *DB) ReplaceSchedule(plans []DayPlan) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM schedule`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO schedule (date, subject_name, target_hours) VALUES (?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range plans {
		for name, h := range p.Subject {
			if h <= 0 {
				continue
			}
			if _, err := stmt.Exec(p.Date.Format("2006-01-02"), name, h); err != nil {
				return err
			}
		}
	}
	if err := stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// ScheduleFrom returns every planned day on/after from (inclusive), sorted.
func (d *DB) ScheduleFrom(from time.Time) ([]DayPlan, error) {
	rows, err := d.db.Query(`SELECT date, subject_name, target_hours FROM schedule
		WHERE date >= ? ORDER BY date, subject_name`, from.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayPlan
	index := map[string]int{}
	for rows.Next() {
		var dateStr, name string
		var h float64
		if err := rows.Scan(&dateStr, &name, &h); err != nil {
			return nil, err
		}
		date, _ := time.Parse("2006-01-02", dateStr)
		i, ok := index[dateStr]
		if !ok {
			i = len(out)
			index[dateStr] = i
			out = append(out, DayPlan{Date: date, Subject: map[string]float64{}})
		}
		out[i].Subject[name] = h
	}
	return out, rows.Err()
}

// PlannedHoursOn returns per-subject planned hours for one day.
func (d *DB) PlannedHoursOn(date time.Time) (map[string]float64, error) {
	rows, err := d.db.Query(`SELECT subject_name, target_hours FROM schedule WHERE date=?`,
		date.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var name string
		var h float64
		if err := rows.Scan(&name, &h); err != nil {
			return nil, err
		}
		out[name] = h
	}
	return out, rows.Err()
}

// SetWeeklyLoad caches the computed weekly load per subject (for display).
func (d *DB) SetWeeklyLoad(name string, hours float64) error {
	_, err := d.db.Exec(`UPDATE subject SET hours_per_week=? WHERE name=?`, hours, name)
	return err
}

// ---------------------------------------------------------------------------
// Activity log
// ---------------------------------------------------------------------------

// LogEntry appends one activity record and keeps the per-subject completion
// flag in sync: a subject counts as completed for the day when logged hours
// cover at least 90% of the planned hours.
func (d *DB) LogEntry(e *Entry) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ds := e.Date.Format("2006-01-02")
	var planned float64
	_ = tx.QueryRow(`SELECT COALESCE(SUM(target_hours),0) FROM schedule
		WHERE date=? AND subject_name=?`, ds, e.SubjectName).Scan(&planned)
	e.Completed = planned > 0 && e.Hours+1e-9 >= planned*0.9
	res, err := tx.Exec(`INSERT INTO activity_log (date, subject_name, hours, completed, note)
		VALUES (?,?,?,?,?)`, ds, e.SubjectName, e.Hours, boolToInt(e.Completed), e.Note)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	// Bump the active-day counter once per calendar day.
	var days int
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT date) FROM activity_log`).Scan(&days); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE profile SET active_days=? WHERE id=1`, days); err != nil {
		return err
	}
	return tx.Commit()
}

// LoggedHoursOn returns per-subject actual hours for one day.
func (d *DB) LoggedHoursOn(date time.Time) (map[string]float64, error) {
	rows, err := d.db.Query(`SELECT subject_name, SUM(hours), MAX(completed) FROM activity_log
		WHERE date=? GROUP BY subject_name`, date.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var name string
		var sum float64
		var done int
		if err := rows.Scan(&name, &sum, &done); err != nil {
			return nil, err
		}
		out[name] = sum
	}
	return out, rows.Err()
}

// DayTotal is one day's aggregate for the GitHub-style grid.
type DayTotal struct {
	Date    time.Time
	Hours   float64
	Planned float64
}

// DailyTotals returns planned vs actual hours for [from, to], including days
// with zero activity (needed to render a gapless activity matrix).
func (d *DB) DailyTotals(from, to time.Time) ([]DayTotal, error) {
	logRows, err := d.db.Query(`SELECT date, SUM(hours) FROM activity_log
		WHERE date BETWEEN ? AND ? GROUP BY date`,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer logRows.Close()
	actual := map[string]float64{}
	for logRows.Next() {
		var ds string
		var h float64
		if err := logRows.Scan(&ds, &h); err != nil {
			return nil, err
		}
		actual[ds] = h
	}

	plRows, err := d.db.Query(`SELECT date, SUM(target_hours) FROM schedule
		WHERE date BETWEEN ? AND ? GROUP BY date`,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer plRows.Close()
	planned := map[string]float64{}
	for plRows.Next() {
		var ds string
		var h float64
		if err := plRows.Scan(&ds, &h); err != nil {
			return nil, err
		}
		planned[ds] = h
	}

	var out []DayTotal
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		ds := day.Format("2006-01-02")
		out = append(out, DayTotal{Date: day, Hours: actual[ds], Planned: planned[ds]})
	}
	return out, nil
}

// CurrentStreak counts consecutive days ending today where the student either
// logged ≥0.5h or covered ≥90% of the planned hours.
func (d *DB) CurrentStreak(today time.Time) (int, error) {
	rows, err := d.db.Query(`SELECT date, SUM(hours) FROM activity_log GROUP BY date`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	active := map[string]bool{}
	for rows.Next() {
		var ds string
		var h float64
		if err := rows.Scan(&ds, &h); err != nil {
			return 0, err
		}
		active[ds] = h >= 0.5
	}
	streak := 0
	for day := today; ; day = day.AddDate(0, 0, -1) {
		ds := day.Format("2006-01-02")
		isActive := active[ds]
		if !isActive {
			var planned, logged float64
			_ = d.db.QueryRow(`SELECT COALESCE(SUM(target_hours),0) FROM schedule WHERE date=?`, ds).Scan(&planned)
			_ = d.db.QueryRow(`SELECT COALESCE(SUM(hours),0) FROM activity_log WHERE date=?`, ds).Scan(&logged)
			isActive = planned > 0 && logged+1e-9 >= planned*0.9
		}
		if !isActive {
			// Today not being active yet should not break yesterday's streak.
			if day.Equal(today) {
				continue
			}
			break
		}
		streak++
	}
	return streak, nil
}

// LastActivityDate returns the most recent logged day (zero time if none).
func (d *DB) LastActivityDate() (time.Time, error) {
	var ds string
	err := d.db.QueryRow(`SELECT MAX(date) FROM activity_log`).Scan(&ds)
	if err != nil || ds == "" {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02", ds)
}

// ---------------------------------------------------------------------------
// Feedback
// ---------------------------------------------------------------------------

// AddFeedback stores one feedback-loop answer.
func (d *DB) AddFeedback(f *Feedback) error {
	res, err := d.db.Exec(`INSERT INTO feedback (date, pace, comment, intensity) VALUES (?,?,?,?)`,
		f.Date.Format("2006-01-02"), f.Pace, f.Comment, f.Intensity)
	if err != nil {
		return err
	}
	f.ID, _ = res.LastInsertId()
	return nil
}

// RecentFeedback returns the newest feedback answers.
func (d *DB) RecentFeedback(limit int) ([]Feedback, error) {
	rows, err := d.db.Query(`SELECT id, date, pace, comment, intensity FROM feedback
		ORDER BY date DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		var f Feedback
		var ds string
		if err := rows.Scan(&f.ID, &ds, &f.Pace, &f.Comment, &f.Intensity); err != nil {
			return nil, err
		}
		f.Date, _ = time.Parse("2006-01-02", ds)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ResetAll wipes user data but keeps the profile row (used by Settings).
func (d *DB) ResetAll() error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM activity_log`, `DELETE FROM schedule`,
		`DELETE FROM subject`, `DELETE FROM feedback`,
		`UPDATE profile SET onboarded=0, active_days=0, next_feedback_day=10, intensity=1.0`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
