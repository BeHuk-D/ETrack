// Package theme centralises every Lipgloss style so screens stay consistent
// and colours are defined exactly once (adaptive light/dark safe).
package theme

import "github.com/charmbracelet/lipgloss"

var (
	// Palette — tuned for dark terminals, degrade gracefully on light ones.
	Accent    = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#58A6FF"} // github blue
	Green     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	GreenDim  = lipgloss.AdaptiveColor{Light: "#4AC26B", Dark: "#238636"}
	Red       = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"}
	Amber     = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"}
	Purple    = lipgloss.AdaptiveColor{Light: "#6639BA", Dark: "#BC8CFF"}
	Text      = lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6EDF3"}
	Muted     = lipgloss.AdaptiveColor{Light: "#656D76", Dark: "#8B949E"}
	Faint     = lipgloss.AdaptiveColor{Light: "#AFB8C1", Dark: "#484F58"}
	PanelBg   = lipgloss.AdaptiveColor{Light: "#F6F8FA", Dark: "#161B22"}
	PanelEdge = lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#30363D"}
	GridEmpty = lipgloss.AdaptiveColor{Light: "#EBECF0", Dark: "#21262D"}
)

// Activity heat scale: 5 levels like GitHub (index 0 = no activity).
var Heat = []lipgloss.AdaptiveColor{
	GridEmpty,
	{Light: "#ACE1E4", Dark: "#0E4429"},
	{Light: "#54D6C6", Dark: "#006D32"},
	{Light: "#26A69A", Dark: "#26A641"},
	{Light: "#0A8043", Dark: "#39D353"},
}

var (
	Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(Accent).
		Padding(0, 1)

	Subtitle = lipgloss.NewStyle().
			Foreground(Muted).
			Italic(true)

	Help = lipgloss.NewStyle().
		Foreground(Faint).
		Padding(0, 1)

	Key = lipgloss.NewStyle().
		Bold(true).
		Foreground(Accent)

	Panel = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(PanelEdge).
		Padding(1, 2)

	PanelActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Accent).
			Padding(1, 2)

	StatValue = lipgloss.NewStyle().
			Bold(true).
			Foreground(Text).
			Align(lipgloss.Center)

	StatLabel = lipgloss.NewStyle().
			Foreground(Muted).
			Align(lipgloss.Center)

	Success = lipgloss.NewStyle().Bold(true).Foreground(Green)
	Error   = lipgloss.NewStyle().Bold(true).Foreground(Red)
	Warn    = lipgloss.NewStyle().Bold(true).Foreground(Amber)

	StepActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(Text).
			Background(lipgloss.AdaptiveColor{Light: "#DDF4FF", Dark: "#1F6FEB33"}).
			Padding(0, 1)

	StepDone = lipgloss.NewStyle().Foreground(Green).Padding(0, 1)
	StepTodo = lipgloss.NewStyle().Foreground(Faint).Padding(0, 1)

	OverlayScrim = lipgloss.NewStyle().
			Foreground(Faint).
			Background(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0D1117"})

	Brand = lipgloss.NewStyle().
		Bold(true).
		Foreground(Accent).
		Background(lipgloss.AdaptiveColor{Light: "#DDF4FF", Dark: "#0D2240"}).
		Padding(0, 1)
)

// PanelWidth returns an inner content width for a bordered panel of given
// outer width (rounded border consumes 2 columns on each side + padding).
func PanelInnerWidth(outer int) int {
	w := outer - 2 /*borders*/ - 2 /*padding*/ - 2
	if w < 10 {
		return 10
	}
	return w
}
