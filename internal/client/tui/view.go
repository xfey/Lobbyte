package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/xfey/lobbyte/internal/protocol"
)

var terminalStyleSuffix = regexp.MustCompile(`(?:\x1b\[[0-?]*[ -/]*[@-~])+$`)

var styles = struct {
	appFrame, homeButton, homeSelected            lipgloss.Style
	muted, status, error, separator, inputSurface lipgloss.Style
	brandLogo, roomLogo, peerBubble, selfBubble   lipgloss.Style
}{
	appFrame: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "238"}),
	homeButton: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "239"}).
		Padding(0, 2),
	homeSelected: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "243", Dark: "245"}).
		Background(lipgloss.AdaptiveColor{Light: "254", Dark: "236"}).
		Padding(0, 2).
		Bold(true),
	muted: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}),
	status: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "245"}),
	error: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"}),
	separator: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "252", Dark: "239"}),
	inputSurface: lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "254", Dark: "236"}),
	brandLogo: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "238", Dark: "250"}),
	roomLogo: lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "248", Dark: "241"}),
	peerBubble: lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "255", Dark: "236"}).
		Padding(0, 1),
	selfBubble: lipgloss.NewStyle().
		Background(lipgloss.AdaptiveColor{Light: "254", Dark: "238"}).
		Padding(0, 1),
}

const (
	roomHeaderHeight   = 4
	commandLayerHeight = 3
	inputSurfaceHeight = 3
	roomStatusHeight   = 1
	roomOuterHeight    = 2
	roomSidePadding    = 2
)

// View renders one adaptive frame using centralized visual styles.
func (model Model) View() string {
	if model.Width < MinWidth || model.Height < MinHeight {
		return "Terminal too small — resize to continue."
	}
	if model.Page == PageRoom {
		return model.roomView()
	}
	if model.Page == PageHome {
		return model.homeView()
	}
	if model.Page == PageWaiting {
		return model.waitingView()
	}
	switch model.Page {
	case PageAdmission:
		return model.admissionView()
	case PageHooks:
		return model.hooksView()
	case PageIdentityError:
		return model.identityErrorView()
	}
	return ""
}

func (model Model) admissionView() string {
	width := model.pageContentWidth()
	copy := strings.Join([]string{
		"Be kind and respectful when chatting.",
		"Do not share private code, credentials, or personal information.",
		"Continue accepts the Community Rules and Privacy Notice.",
	}, "\n")
	blocks := []string{
		logoView(brandLogoLines, styles.brandLogo),
		centeredBlock(copy, width, lipgloss.NewStyle()),
	}
	if model.RulesURL != "" && model.PrivacyURL != "" {
		blocks = append(blocks, documentLinksView(width, [][2]string{
			{"Rules", model.RulesURL},
			{"Privacy", model.PrivacyURL},
		}))
	}
	if model.Notice != "" {
		blocks = append(blocks, centeredBlock(model.Notice, width, styles.error))
	}
	blocks = append(blocks, splitActionView(
		width, []string{"Continue", "Quit"}, model.AdmissionFocus, 2,
	))
	return model.standardPageView(blocks...)
}

func (model Model) homeView() string {
	return model.landingView(false)
}

func (model Model) waitingView() string {
	return model.landingView(true)
}

func (model Model) landingView(waiting bool) string {
	innerWidth := max(1, model.Width-2)
	contentWidth := max(1, innerWidth-roomSidePadding*2)
	innerHeight := max(1, model.Height-roomOuterHeight)
	buttonGroupWidth := min(42, contentWidth)
	secondaryGap := 2
	secondaryWidth := (buttonGroupWidth - secondaryGap) / 2

	labels := []string{"Match", "Connect", "Quit"}
	buttons := make([]string, 0, len(labels))
	for index, label := range labels {
		style := styles.homeButton
		if index == model.HomeFocus || waiting && index == 2 {
			style = styles.homeSelected
		}
		width := buttonGroupWidth
		if index > 0 && !waiting {
			width = secondaryWidth
		}
		buttons = append(buttons, style.Width(max(1, width-2)).Align(lipgloss.Center).Render(label))
	}
	context := ""
	primaryAction := buttons[0]
	secondaryActions := lipgloss.JoinHorizontal(lipgloss.Center, buttons[1], strings.Repeat(" ", secondaryGap), buttons[2])
	if waiting {
		statusText := model.Spinner.View() + "  Looking for another human…"
		if model.Reconnecting {
			statusText = model.Spinner.View() + "  Getting back online…"
		}
		primaryAction = styles.homeSelected.
			Width(max(1, buttonGroupWidth-2)).
			Align(lipgloss.Center).
			Render("Back")
		secondaryActions = lipgloss.NewStyle().
			Width(buttonGroupWidth).
			Height(3).
			Align(lipgloss.Center).
			AlignVertical(lipgloss.Center).
			Render(styles.muted.Render(statusText))
	} else if model.Notice != "" {
		context = styles.muted.Render(model.Notice)
	}
	parts := []string{
		logoView(brandLogoLines, styles.brandLogo),
		"",
		styles.muted.Render("Meet another human while your agent works."),
		context,
		primaryAction,
		secondaryActions,
	}
	main := lipgloss.NewStyle().
		Width(contentWidth).
		Height(innerHeight).
		MaxHeight(innerHeight).
		Align(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(strings.Join(parts, "\n"))
	return styles.appFrame.
		Width(innerWidth).
		Height(innerHeight).
		Render(roomRow(main, innerWidth, innerHeight))
}

func (model Model) pageContentWidth() int {
	return max(1, model.Width-2-roomSidePadding*2)
}

// standardPageView keeps non-room flows on the same full-terminal canvas as
// the landing and room pages. It removes spacing before dropping content when
// the terminal is at the supported minimum size.
func (model Model) standardPageView(blocks ...string) string {
	innerWidth := max(1, model.Width-2)
	contentWidth := model.pageContentWidth()
	innerHeight := max(1, model.Height-roomOuterHeight)
	visible := make([]string, 0, len(blocks))
	contentHeight := 0
	for _, block := range blocks {
		if block == "" {
			continue
		}
		visible = append(visible, block)
		contentHeight += lipgloss.Height(block)
	}
	gap := 0
	if gapCount := len(visible) - 1; gapCount > 0 {
		gap = min(2, max(0, (innerHeight-contentHeight)/gapCount))
	}
	separator := strings.Repeat("\n", gap+1)
	main := lipgloss.NewStyle().
		Width(contentWidth).
		MaxWidth(contentWidth).
		Height(innerHeight).
		MaxHeight(innerHeight).
		Align(lipgloss.Center).
		AlignVertical(lipgloss.Center).
		Render(strings.Join(visible, separator))
	return styles.appFrame.
		Width(innerWidth).
		Height(innerHeight).
		Render(roomRow(main, innerWidth, innerHeight))
}

func centeredBlock(text string, width int, style lipgloss.Style) string {
	wrapped := ansi.Wrap(text, max(1, width), "")
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Align(lipgloss.Center).
		Render(style.Render(wrapped))
}

func documentLinksView(width int, links [][2]string) string {
	lines := make([]string, 0, len(links)*2)
	for _, link := range links {
		prefix := fmt.Sprintf("%-7s  ", link[0])
		if lipgloss.Width(prefix)+lipgloss.Width(link[1]) <= width {
			lines = append(lines, centeredLine(prefix+styles.muted.Render(link[1]), width))
			continue
		}
		lines = append(lines, centeredLine(strings.TrimSpace(prefix), width))
		lines = append(lines, centeredBlock(link[1], width, styles.muted))
	}
	return strings.Join(lines, "\n")
}

func splitActionView(width int, labels []string, focus, primaryWeight int) string {
	const gap = 2
	groupWidth := min(42, width)
	available := max(2, groupWidth-gap)
	firstWidth := available / 2
	if primaryWeight > 1 {
		firstWidth = available * primaryWeight / (primaryWeight + 1)
	}
	widths := []int{firstWidth, available - firstWidth}
	buttons := make([]string, 0, len(labels))
	for index, label := range labels {
		style := styles.homeButton
		if index == focus {
			style = styles.homeSelected
		}
		buttons = append(buttons, style.
			Width(max(1, widths[index]-2)).
			Align(lipgloss.Center).
			Render(label))
	}
	group := lipgloss.JoinHorizontal(lipgloss.Center, buttons[0], strings.Repeat(" ", gap), buttons[1])
	return centeredLine(group, width)
}

func singleActionView(width int, label string) string {
	button := styles.homeSelected.
		Width(max(1, min(42, width)-2)).
		Align(lipgloss.Center).
		Render(label)
	return centeredLine(button, width)
}

func centeredLine(line string, width int) string {
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Align(lipgloss.Center).Render(line)
}

func (model Model) roomView() string {
	innerWidth := max(1, model.Width-2)
	contentWidth := max(1, innerWidth-roomSidePadding*2)
	innerHeight := max(1, model.Height-roomOuterHeight)

	parts := []string{
		roomRow(model.peerHeaderView(contentWidth), innerWidth, roomHeaderHeight),
		roomRow(model.conversationView(), innerWidth, model.Viewport.Height),
		roomRow(model.inputView(contentWidth), innerWidth, inputSurfaceHeight),
		roomRow(model.roomStatusView(contentWidth), innerWidth, roomStatusHeight),
	}
	content := lipgloss.NewStyle().
		Width(innerWidth).
		Height(innerHeight).
		MaxHeight(innerHeight).
		Render(strings.Join(parts, "\n"))
	return styles.appFrame.
		Width(innerWidth).
		Height(innerHeight).
		Render(content)
}

func (model Model) conversationView() string {
	height := model.Viewport.Height
	if height <= 0 {
		return ""
	}
	lines := strings.Split(model.Viewport.View(), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if !model.CommandOpen {
		return strings.Join(lines[:height], "\n")
	}
	commandLines := strings.Split(model.commandView(), "\n")
	overlayHeight := min(commandLayerHeight, height)
	copy(lines[height-overlayHeight:height], commandLines[:overlayHeight])
	return strings.Join(lines[:height], "\n")
}

func (model Model) commandView() string {
	descriptions := map[string]string{
		"/next": "switch to another room", "/exit": "return to home", "/block": "block and switch rooms",
	}
	lines := make([]string, 0, commandLayerHeight)
	for index, name := range model.filteredCommands() {
		prefix := "  "
		if index == model.CommandFocus {
			prefix = "› "
		}
		lines = append(lines, prefix+fmt.Sprintf("%-6s", name)+"  "+styles.muted.Render(descriptions[name]))
	}
	if len(lines) == 0 {
		lines = append(lines, styles.muted.Render("No commands"))
	}
	for len(lines) < commandLayerHeight {
		lines = append([]string{""}, lines...)
	}
	return strings.Join(lines[:commandLayerHeight], "\n")
}

func (model Model) peerHeaderView(width int) string {
	identity := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Builder #%04d", model.PeerBuilderID))
	metadata := lipgloss.NewStyle().
		Height(2).
		Render(identity + "\n" + styles.muted.Render(presenceText(model.PeerPresence)))
	peer := lipgloss.JoinHorizontal(lipgloss.Center, avatarView(model.PeerBuilderID), "  ", metadata)
	header := peer
	logo := logoView(roomLogoLines, styles.roomLogo)
	if lipgloss.Width(peer)+logoGap+logoWidth(roomLogoLines) <= width {
		header = lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Width(width-logoWidth(roomLogoLines)).Render(peer),
			logo,
		)
	}
	header = lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render(header)
	divider := styles.separator.Render(strings.Repeat("─", width))
	return header + "\n" + divider
}

func (model Model) inputView(width int) string {
	line := fillInputRow(model.Input.View(), width, styles.inputSurface)
	blank := styles.inputSurface.Width(width).Render("")
	return strings.Join([]string{blank, line, blank}, "\n")
}

func fillInputRow(line string, width int, surface lipgloss.Style) string {
	return surface.Width(width).MaxWidth(width).Render(trimTerminalPadding(line))
}

// trimTerminalPadding removes only the unstyled fill cells generated by the
// textarea viewport. Keeping them would make the outer input surface believe
// the row is already full-width, so its background could not color the rest of
// the line after an inner ANSI reset.
func trimTerminalPadding(line string) string {
	suffixStart := len(line)
	if location := terminalStyleSuffix.FindStringIndex(line); location != nil && location[1] == len(line) {
		suffixStart = location[0]
	}
	paddingStart := suffixStart
	for paddingStart > 0 && line[paddingStart-1] == ' ' {
		paddingStart--
	}
	return line[:paddingStart] + line[suffixStart:]
}

func (model Model) roomStatusView(width int) string {
	var left string
	switch {
	case model.InputError != "":
		left = styles.error.Render(model.InputError)
	case model.Reconnecting:
		left = styles.status.Render("reconnecting")
	}
	right := styles.status.Render(presenceText(model.SelfPresence))
	return spread(left, right, width)
}

func roomRow(content string, innerWidth, height int) string {
	return lipgloss.NewStyle().
		Width(innerWidth).
		MaxWidth(innerWidth).
		Height(max(0, height)).
		MaxHeight(max(0, height)).
		Padding(0, roomSidePadding).
		Render(content)
}

func spread(left, right string, width int) string {
	leftWidth, rightWidth := lipgloss.Width(left), lipgloss.Width(right)
	if leftWidth+rightWidth > width {
		right = ansi.Truncate(right, max(0, width-leftWidth-1), "…")
		rightWidth = lipgloss.Width(right)
	}
	return left + strings.Repeat(" ", max(1, width-leftWidth-rightWidth)) + right
}

func (model Model) messageHistoryView() string {
	width := max(1, model.Viewport.Width)
	lines := make([]string, 0, len(model.Messages)*2+2)
	if model.trimmed {
		lines = append(lines, lipgloss.NewStyle().Width(width).Align(lipgloss.Center).
			Render(styles.muted.Render("Earlier messages removed")), "")
	}
	for _, message := range model.Messages {
		bubbleWidth := min(max(18, width*2/3), width)
		textWidth := max(1, bubbleWidth-2)
		wrapped := ansi.Wrap(message.Text, textWidth, "")
		actualWidth := maxLineWidth(wrapped)
		style, alignment := styles.peerBubble, lipgloss.Left
		if message.SenderID == model.BuilderID {
			style, alignment = styles.selfBubble, lipgloss.Right
		}
		bubble := style.Width(actualWidth + 2).Render(wrapped)
		lines = append(lines, lipgloss.NewStyle().Width(width).Align(alignment).Render(bubble), "")
	}
	return strings.Join(lines, "\n")
}

func maxLineWidth(text string) int {
	width := 1
	for _, line := range strings.Split(text, "\n") {
		width = max(width, lipgloss.Width(line))
	}
	return width
}

func (model Model) hooksView() string {
	width := model.pageContentWidth()
	blocks := []string{logoView(brandLogoLines, styles.brandLogo)}
	if !model.HooksChecked {
		blocks = append(blocks, centeredBlock(model.Spinner.View()+"  Connecting agents…", width, styles.muted))
	} else if len(model.HookResults) == 0 {
		blocks = append(blocks, centeredBlock("No supported agents found.", width, lipgloss.NewStyle()))
	} else {
		blocks = append(blocks, hookResultsView(model.HookResults, width))
		reviewRequired := false
		for _, result := range model.HookResults {
			if result.Reason == "review required" {
				reviewRequired = true
				break
			}
		}
		if reviewRequired {
			blocks = append(blocks, centeredBlock("Open /hooks in Codex and trust Lobbyte.", width, lipgloss.NewStyle()))
		}
	}
	blocks = append(blocks, singleActionView(width, "Back"))
	return model.standardPageView(blocks...)
}

func hookResultsView(results []HookResult, width int) string {
	rows := make([]string, 0, len(results)*2)
	for _, result := range results {
		main := fmt.Sprintf("%-12s  %-10s", result.Agent, result.Status)
		reason := ""
		if result.Reason != "" {
			reason = styles.muted.Render("(" + result.Reason + ")")
		}
		if reason == "" || lipgloss.Width(main)+1+lipgloss.Width(reason) <= width {
			rows = append(rows, strings.TrimRight(main+" "+reason, " "))
			continue
		}
		rows = append(rows, strings.TrimRight(main, " "), "              "+reason)
	}
	groupWidth := 1
	for _, row := range rows {
		groupWidth = max(groupWidth, lipgloss.Width(row))
	}
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Align(lipgloss.Center).
		Render(lipgloss.NewStyle().Width(min(width, groupWidth)).Render(strings.Join(rows, "\n")))
}

func (model Model) identityErrorView() string {
	width := model.pageContentWidth()
	blocks := []string{
		logoView(brandLogoLines, styles.brandLogo),
		centeredBlock("Identity could not be read.", width, lipgloss.NewStyle()),
	}
	if model.Notice != "" {
		blocks = append(blocks, centeredBlock(model.Notice, width, styles.error))
	}
	blocks = append(blocks, splitActionView(
		width, []string{"Create new identity", "Quit"}, model.IdentityFocus, 2,
	))
	return model.standardPageView(blocks...)
}

func presenceText(presence protocol.Presence) string {
	if presence.State == protocol.AgentIdle {
		return "idle"
	}
	source := "Codex"
	if presence.Source == protocol.SourceClaudeCode {
		source = "Claude Code"
	}
	switch presence.State {
	case protocol.AgentPrompting:
		return "prompting with " + source
	case protocol.AgentWaiting:
		return "waiting for " + source
	case protocol.AgentDone:
		return "done with " + source
	default:
		return "idle"
	}
}

func min(first, second int) int {
	if first < second {
		return first
	}
	return second
}
