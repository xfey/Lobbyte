package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const logoGap = 2

var brandLogoLines = [3]string{
	"╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗",
	"║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣ ",
	"╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝",
}

var roomLogoLines = [3]string{
	"┬  ┌─┐┌┐ ┌┐ ┬ ┬┌┬┐┌─┐",
	"│  │ │├┴┐├┴┐└┬┘ │ ├┤ ",
	"┴─┘└─┘└─┘└─┘ ┴  ┴ └─┘",
}

// logoView renders the selected CALVIN_S TheDraw wordmark. Keeping the final
// cells local makes rendering deterministic and avoids shipping a generator.
func logoView(lines [3]string, style lipgloss.Style) string {
	return style.Render(strings.Join(lines[:], "\n"))
}

func logoWidth(lines [3]string) int {
	return lipgloss.Width(lines[0])
}
