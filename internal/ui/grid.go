// Package ui — shared visual widgets used by several screens.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"etrack/internal/storage"
	"etrack/internal/ui/theme"
)

// ---------------------------------------------------------------------------
// GitHub-style activity matrix
// ---------------------------------------------------------------------------

// GridCell is one square of the activity matrix.
type GridCell struct {
	Date  time.Time
	Hours float64 // actual studied hours that day
}

const (
	gridRows    = 7    // Mon..Sun
	cellChar    = "■ " // two terminal cells wide → looks square
	cellCharOff = "░ " // dim glyph for future / non-study days
	maxHeatRef  = 6.0  // hours at which heat reaches level 4
)

// HeatLevel maps hours to a 0..4 scale, proportional to real effort.
func HeatLevel(hours float64) int {
	switch {
	case hours <= 0:
		return 0
	case hours < maxHeatRef*0.25:
		return 1
	case hours < maxHeatRef*0.5:
		return 2
	case hours < maxHeatRef*0.85:
		return 3
	default:
		return 4
	}
}

// RenderGrid draws weeks as columns (left→right) and weekdays as rows.
// It returns the rendered string plus the number of lines it occupies.
func RenderGrid(days []storage.DayTotal, today time.Time, width int) (string, int) {
	const colsMax = 18 // ~18 weeks fits in most terminals; trimmed below
	if len(days) == 0 {
		return theme.Subtitle.Render("  нет данных — план ещё не построен"), 1
	}
	end := today
	start := end.AddDate(0, 0, -(colsMax*7 - 1))
	var win []storage.DayTotal
	for _, d := range days {
		if !d.Date.Before(start) && !d.Date.After(end) {
			win = append(win, d)
		}
	}
	if len(win) == 0 {
		win = days
	}

	// Bucket into weeks aligned so each column starts on Monday.
	type week struct {
		cells [gridRows]GridCell
		has   [gridRows]bool
	}
	var weeks []week
	idx := map[string]*week{}
	keyFor := func(t time.Time) string {
		y, m, d := t.Date()
		monday := t.AddDate(0, 0, -int((t.Weekday()+6)%7))
		_ = y
		_ = m
		_ = d
		return monday.Format("2006-01-02")
	}
	for _, d := range win {
		k := keyFor(d.Date)
		w, ok := idx[k]
		if !ok {
			weeks = append(weeks, week{})
			w = &weeks[len(weeks)-1]
			idx[k] = w
		}
		row := int((d.Date.Weekday() + 6) % 7) // Mon=0 … Sun=6
		w.cells[row] = GridCell{Date: d.Date, Hours: d.Hours}
		w.has[row] = true
	}

	wdLabels := []string{"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

	var b strings.Builder
	// Month header line.
	months := make([]string, len(weeks))
	lastMonth := ""
	for i, w := range weeks {
		for r := 0; r < gridRows; r++ {
			if w.has[r] {
				mn := w.cells[r].Date.Format("янв")
				if mn != lastMonth {
					months[i] = mn
					lastMonth = mn
				}
				break
			}
		}
	}
	b.WriteString("   ")
	for _, m := range months {
		if m == "" {
			m = "    "
		} else {
			m = padOrTrunc(m, 4)
		}
		b.WriteString(m)
	}
	b.WriteString("\n")

	for r := 0; r < gridRows; r++ {
		b.WriteString(theme.Muted.Render(padOrTrunc(wdLabels[r], 2)) + " ")
		for wi := range weeks {
			var cell string
			if !weeks[wi].has[r] {
				cell = lipgloss.NewStyle().Foreground(theme.Faint).Render(cellCharOff)
			} else {
				c := weeks[wi].cells[r]
				isFuture := c.Date.After(today)
				lvl := HeatLevel(c.Hours)
				st := lipgloss.NewStyle().Foreground(theme.Heat[lvl])
				if isFuture {
					cell = lipgloss.NewStyle().Foreground(theme.GridEmpty).Render(cellCharOff)
				} else {
					cell = st.Render(cellChar)
				}
				if sameDay(c.Date, today) {
					cell = lipgloss.NewStyle().Bold(true).Foreground(theme.Accent).Render("▢ ")
				}
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}

	// Legend.
	b.WriteString("   ")
	b.WriteString(theme.Muted.Render("меньше "))
	for l := 0; l <= 4; l++ {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Heat[l]).Render(cellChar))
	}
	b.WriteString(theme.Muted.Render(" больше"))
	lines := strings.Count(b.String(), "\n")
	return b.String(), lines
}

// ---------------------------------------------------------------------------
// Progress bar
// ---------------------------------------------------------------------------

// ProgressBar renders a fixed-width block meter [████░░░░░░] 4.5/6 ч.
func ProgressBar(frac float64, width int, label string) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(float64(width)*frac + 0.5)
	col := theme.Green
	switch {
	case frac < 0.34:
		col = theme.Red
	case frac < 0.7:
		col = theme.Amber
	}
	bar := lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("█", filled)) +
		lipgloss.NewStyle().Foreground(theme.GridEmpty).Render(strings.Repeat("░", width-filled))
	pct := fmt.Sprintf("%3.0f%%", frac*100)
	if label != "" {
		return bar + "  " + theme.Muted.Render(label+" ") + theme.Text.Render(pct)
	}
	return bar + "  " + theme.Text.Render(pct)
}

// ---------------------------------------------------------------------------
// misc helpers
// ---------------------------------------------------------------------------

func sameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func padOrTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}
