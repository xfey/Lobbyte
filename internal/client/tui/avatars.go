package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// avatarDefinition keeps source and display adaptation metadata next to each
// community preview asset so later visual edits remain traceable.
type avatarDefinition struct {
	Name       string
	Artist     string
	Source     string
	Adaptation string
	Lines      [3]string
	Color      lipgloss.AdaptiveColor
}

// avatarDefinitions contains the compact animal set used in room headers.
var avatarDefinitions = []avatarDefinition{
	{
		Name: "cat-face", Artist: "unknown", Source: "https://www.asciiart.eu/animals/cats",
		Adaptation: "verbatim three-line artwork",
		Lines:      [3]string{`|\---/|`, `| o_o |`, ` \_^_/`},
		Color:      lipgloss.AdaptiveColor{Light: "173", Dark: "217"},
	},
	{
		Name: "cat", Artist: "unknown", Source: "https://www.asciiart.eu/animals/cats",
		Adaptation: "verbatim three-line artwork",
		Lines:      [3]string{` /\_/\`, `( o.o )`, ` > ^ <`},
		Color:      lipgloss.AdaptiveColor{Light: "110", Dark: "151"},
	},
	{
		Name: "duck", Artist: "unknown", Source: "https://www.asciiart.eu/animals/birds-water",
		Adaptation: "verbatim three-line artwork",
		Lines:      [3]string{`   (@_`, `\\\_\`, `<____)`},
		Color:      lipgloss.AdaptiveColor{Light: "179", Dark: "221"},
	},
	{
		Name: "butterfly", Artist: "unknown", Source: "https://www.asciiart.eu/animals/insects/butterflies",
		Adaptation: "descriptive text removed from the original three-line artwork",
		Lines:      [3]string{`(\ o /)`, ` ))¥((`, `(/ ^ \)`},
		Color:      lipgloss.AdaptiveColor{Light: "176", Dark: "219"},
	},
	{
		Name: "owl", Artist: "unknown", Source: "https://www.asciiart.eu/animals/birds-land",
		Adaptation: "fourth foot line omitted for the three-line preview",
		Lines:      [3]string{` /\ /\`, `((ovo))`, `():::()`},
		Color:      lipgloss.AdaptiveColor{Light: "136", Dark: "180"},
	},
	{
		Name: "fox", Artist: "cp97", Source: "https://www.asciiart.eu/animals/other-land",
		Adaptation: "signature line omitted for the three-line preview",
		Lines:      [3]string{` ^...^`, `<_* *_>`, `  \_/`},
		Color:      lipgloss.AdaptiveColor{Light: "166", Dark: "209"},
	},
	{
		Name: "pixel-mouse", Artist: "Ivo", Source: "https://github.com/stark/Color-Scripts/blob/master/color-scripts/mouseface",
		Adaptation: "verbatim three-line pixel face",
		Lines:      [3]string{` █   █ `, `  ■ ■  `, `  =■=  `},
		Color:      lipgloss.AdaptiveColor{Light: "167", Dark: "210"},
	},
	{
		Name: "pixel-mouse-mask", Artist: "Ivo", Source: "https://github.com/stark/Color-Scripts/blob/master/color-scripts/mouseface",
		Adaptation: "verbatim alternate three-line pixel face",
		Lines:      [3]string{` █=@=█ `, `  ■ ■  `, `  =■=  `},
		Color:      lipgloss.AdaptiveColor{Light: "68", Dark: "111"},
	},
}

func avatarView(builderID int64) string {
	avatar := avatarForBuilder(builderID)
	return lipgloss.NewStyle().Foreground(avatar.Color).Render(strings.Join(avatar.Lines[:], "\n"))
}

func avatarForBuilder(builderID int64) avatarDefinition {
	value := uint64(builderID)
	value ^= value >> 12
	value ^= value << 25
	value ^= value >> 27
	return avatarDefinitions[int(value%uint64(len(avatarDefinitions)))]
}
