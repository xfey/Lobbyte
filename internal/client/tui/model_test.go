package tui

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/xfey/lobbyte/internal/protocol"
	"github.com/xfey/lobbyte/internal/textcheck"
)

func TestHomeRequiresExplicitStart(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Width, model.Height = 80, 24
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)
	if result.Page != PageWaiting {
		t.Fatalf("page = %d, want waiting", result.Page)
	}
	effects := result.DrainEffects()
	if len(effects) != 1 || effects[0].Action != "connect-and-match" {
		t.Fatalf("unexpected effects: %#v", effects)
	}
	if command == nil {
		t.Fatal("waiting animation did not start")
	}
}

func TestWaitingBackAndEscapeCancelMatching(t *testing.T) {
	t.Parallel()

	for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeyEsc} {
		model := NewModel(true)
		model.Page, model.Width, model.Height = PageWaiting, 80, 24
		updated, _ := model.Update(tea.KeyMsg{Type: key})
		model = updated.(Model)
		if effects := model.DrainEffects(); model.Page != PageHome || len(effects) != 1 || effects[0].Type != protocol.EventMatchCancel {
			t.Fatalf("waiting key %v: page=%d effects=%#v", key, model.Page, effects)
		}
	}
}

func TestPageFocusNavigation(t *testing.T) {
	t.Parallel()

	model := NewModel(false)
	model.Width, model.Height = 80, 24
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	if model.AdmissionFocus != 1 {
		t.Fatalf("admission focus = %d", model.AdmissionFocus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(Model)
	if model.AdmissionFocus != 0 {
		t.Fatalf("admission focus = %d after left", model.AdmissionFocus)
	}

	model.Page = PageHome
	for index, step := range []struct {
		key  tea.KeyType
		want int
	}{{tea.KeyDown, 1}, {tea.KeyRight, 2}, {tea.KeyUp, 0}, {tea.KeyDown, 1}, {tea.KeyLeft, 1}} {
		updated, _ = model.Update(tea.KeyMsg{Type: step.key})
		model = updated.(Model)
		if model.HomeFocus != step.want {
			t.Fatalf("home step %d focus = %d, want %d", index, model.HomeFocus, step.want)
		}
	}

	model.Page = PageIdentityError
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.IdentityFocus != 1 {
		t.Fatalf("identity focus = %d", model.IdentityFocus)
	}
}

func TestHookPageReturnsHomeWithEnterOrEscape(t *testing.T) {
	t.Parallel()

	for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeyEsc} {
		model := NewModel(true)
		model.Page, model.Width, model.Height = PageHooks, 80, 24
		updated, _ := model.Update(tea.KeyMsg{Type: key})
		if result := updated.(Model); result.Page != PageHome {
			t.Fatalf("hook page key %v returned page %d", key, result.Page)
		}
	}
}

func TestResizeUsesSmallTerminalPauseWithoutLosingInput(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageRoom
	model.Input.SetValue("draft")
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	result := updated.(Model)
	if !strings.Contains(result.View(), "Terminal too small") || result.Input.Value() != "draft" {
		t.Fatalf("small view or input state is wrong: %q %q", result.View(), result.Input.Value())
	}
}

func TestSmallTerminalPausesMouseInteraction(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageRoom
	model.resize(60, 18)
	for index := 0; index < 30; index++ {
		model.appendMessage(Message{SenderID: int64(index%2 + 1), Text: "line"})
	}
	model.Viewport.GotoBottom()
	model.Width, model.Height = 20, 5
	before := model.Viewport.YOffset
	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if after := updated.(Model).Viewport.YOffset; after != before {
		t.Fatalf("small terminal mouse changed viewport: %d -> %d", before, after)
	}
}

func TestEscClosesCommandMenuWithoutClearingInput(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 80, 24
	model.Input.SetValue("/")
	model.CommandOpen = true
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	result := updated.(Model)
	if result.CommandOpen || result.Input.Value() != "/" {
		t.Fatalf("command state = %v, input = %q", result.CommandOpen, result.Input.Value())
	}
}

func TestSlashCommandsLeaveImmediatelyWithoutBecomingMessages(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		command string
		event   protocol.EventType
		page    Page
	}{
		{command: "/next", event: protocol.EventRoomNext, page: PageWaiting},
		{command: "/exit", event: protocol.EventRoomExit, page: PageHome},
		{command: "/block", event: protocol.EventRoomBlock, page: PageWaiting},
	} {
		model := NewModel(true)
		model.Page, model.RoomID, model.Width, model.Height = PageRoom, "room", 80, 24
		model.Messages = []Message{{SenderID: 2, Text: "old message"}}
		model.Input.SetValue(test.command)
		model.CommandOpen = true
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		result := updated.(Model)
		effects := result.DrainEffects()
		if result.Page != test.page || result.RoomID != "" || len(result.Messages) != 0 ||
			result.Input.Value() != "" || len(effects) != 1 || effects[0].Type != test.event {
			t.Fatalf("%s result: page=%d room=%q messages=%#v input=%q effects=%#v",
				test.command, result.Page, result.RoomID, result.Messages, result.Input.Value(), effects)
		}
	}
}

func TestUnknownSlashCommandIsPreserved(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.Width, model.Height = PageRoom, "room", 80, 24
	model.Input.SetValue("/unknown")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)
	if result.Input.Value() != "/unknown" || result.InputError != "Unknown command" || len(result.Effects) != 0 {
		t.Fatalf("unknown command: input=%q error=%q effects=%#v",
			result.Input.Value(), result.InputError, result.Effects)
	}
}

func TestPartialSlashCommandRequiresExplicitCompletion(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.Width, model.Height = PageRoom, "room", 80, 24
	model.Input.SetValue("/n")
	model.CommandOpen = true
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)
	if result.Page != PageRoom || result.RoomID != "room" || result.Input.Value() != "/n" ||
		result.InputError != "Unknown command" || len(result.Effects) != 0 {
		t.Fatalf("partial command executed: page=%d room=%q input=%q error=%q effects=%#v",
			result.Page, result.RoomID, result.Input.Value(), result.InputError, result.Effects)
	}
}

func TestMessageAppearsOnlyAfterRelayEvent(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Width, model.Height = 80, 24
	model.Input.SetValue("hello")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(Model)
	if len(result.Messages) != 0 || result.Input.Value() != "hello" {
		t.Fatalf("message shown or input cleared before Relay: %#v %q", result.Messages, result.Input.Value())
	}
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{RoomID: "room", SenderID: 1, Text: "hello"})
	if err := result.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Input.Value() != "" {
		t.Fatalf("relay message not applied: %#v %q", result.Messages, result.Input.Value())
	}
}

func TestMessageCannotBeSubmittedTwiceBeforeRelayAcceptance(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Input.SetValue("hello")
	model = model.submitInput()
	first := model.DrainEffects()
	model = model.submitInput()
	second := model.DrainEffects()
	if len(first) != 1 || first[0].Type != protocol.EventMessageSend || len(second) != 0 ||
		model.InputError != "Message is sending" || model.Input.Value() != "hello" {
		t.Fatalf("duplicate submit: first=%#v second=%#v error=%q input=%q",
			first, second, model.InputError, model.Input.Value())
	}
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 1, Text: "hello",
	})
	if err := model.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	if model.InputError != "" || model.Input.Value() != "" {
		t.Fatalf("accepted message retained pending UI: error=%q input=%q",
			model.InputError, model.Input.Value())
	}
}

func TestMessageSendFailureAllowsRetryAndKeepsDraft(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Input.SetValue("hello")
	model = model.submitInput()
	_ = model.DrainEffects()
	model.MessageSendFailed()
	if model.Input.Value() != "hello" || model.InputError != "Message not sent." {
		t.Fatalf("failed draft: input=%q error=%q", model.Input.Value(), model.InputError)
	}
	model = model.submitInput()
	if effects := model.DrainEffects(); len(effects) != 1 || effects[0].Type != protocol.EventMessageSend {
		t.Fatalf("retry effects = %#v", effects)
	}
}

func TestMessageSendFailureWithoutPendingMessageIsIgnored(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.InputError = PageRoom, "existing error"
	model.MessageSendFailed()
	if model.InputError != "existing error" {
		t.Fatalf("unrelated error was overwritten: %q", model.InputError)
	}
}

func TestViewKeepsPeerAtTopAndSelfAtBottom(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 80, 24
	model.PeerBuilderID = 23
	model.PeerPresence = protocol.Presence{State: protocol.AgentWaiting, Source: protocol.SourceCodex}
	model.SelfPresence = protocol.Presence{State: protocol.AgentPrompting, Source: protocol.SourceClaudeCode}
	model.resize(80, 24)
	view := model.View()
	if strings.Index(view, "Builder #0023") > strings.Index(view, "prompting with Claude Code") {
		t.Fatalf("peer is not above self status:\n%s", view)
	}
}

func TestRoomHeaderShowsIdleWithoutAgentSource(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.PeerBuilderID = PageRoom, 23
	model.PeerPresence = protocol.Presence{State: protocol.AgentIdle}
	model.resize(80, 24)
	view := normalizedView(model.View())
	if !strings.Contains(view, "Builder #0023") || !strings.Contains(view, "idle") ||
		strings.Contains(view, "idle with") || strings.Contains(view, "idle for") {
		t.Fatalf("idle room header is not source-free:\n%s", view)
	}
}

func TestCommunityAvatarPreviewSetIsThreeLineAndDeterministic(t *testing.T) {
	t.Parallel()

	if len(avatarDefinitions) != 8 {
		t.Fatalf("avatar preview set has %d candidates, want 8", len(avatarDefinitions))
	}
	names := make(map[string]struct{}, len(avatarDefinitions))
	for _, avatar := range avatarDefinitions {
		if avatar.Name == "" || avatar.Source == "" || avatar.Adaptation == "" {
			t.Fatalf("avatar metadata is incomplete: %#v", avatar)
		}
		if _, exists := names[avatar.Name]; exists {
			t.Fatalf("duplicate avatar name %q", avatar.Name)
		}
		names[avatar.Name] = struct{}{}
		for lineNumber, line := range avatar.Lines {
			if line == "" || strings.Contains(line, "\n") || ansi.StringWidth(line) > 24 {
				t.Fatalf("avatar %q line %d is invalid: %q", avatar.Name, lineNumber+1, line)
			}
		}
	}
	for builderID := int64(1); builderID <= 100; builderID++ {
		first, second := avatarForBuilder(builderID), avatarForBuilder(builderID)
		if first.Name != second.Name {
			t.Fatalf("Builder %d avatar changed: %q -> %q", builderID, first.Name, second.Name)
		}
	}
}

func TestLogosUseThreeLineWeightHierarchyAndRoomPlacementIsResponsive(t *testing.T) {
	t.Parallel()

	for name, lines := range map[string][3]string{"brand": brandLogoLines, "room": roomLogoLines} {
		for lineNumber, line := range lines {
			if width := ansi.StringWidth(line); width != logoWidth(lines) {
				t.Fatalf("%s logo line %d uses %d cells, want %d: %q",
					name, lineNumber+1, width, logoWidth(lines), line)
			}
		}
	}
	if !strings.Contains(strings.Join(brandLogoLines[:], ""), "═") ||
		strings.Contains(strings.Join(roomLogoLines[:], ""), "═") {
		t.Fatal("brand logo must use double strokes while room logo remains single-stroke")
	}

	model := NewModel(true)
	model.PeerBuilderID = 23
	if narrow := ansi.Strip(model.peerHeaderView(42)); strings.Contains(narrow, roomLogoLines[0]) {
		t.Fatalf("room logo should be hidden when peer information needs its space:\n%s", narrow)
	}
	wide := ansi.Strip(model.peerHeaderView(74))
	if !strings.Contains(wide, roomLogoLines[0]) || !strings.Contains(wide, "Builder #0023") {
		t.Fatalf("wide room header does not show peer and logo:\n%s", wide)
	}
}

func TestMessageBubblesUseFilledSurfacesWithoutBorders(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.BuilderID, model.PeerBuilderID = PageRoom, 1, 2
	model.Messages = []Message{
		{SenderID: 2, Text: "A short reply"},
		{SenderID: 1, Text: "A longer local message that must wrap inside its own bubble."},
	}
	model.resize(48, 20)
	view := model.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "A short reply") || !strings.Contains(plain, "A longer local message") {
		t.Fatalf("bubble messages are missing:\n%s", plain)
	}
	if strings.Contains(model.messageHistoryView(), "╭") || strings.Contains(model.messageHistoryView(), "┌") {
		t.Fatalf("message bubbles still use borders:\n%s", plain)
	}
	if _, _, _, alpha := styles.peerBubble.GetBackground().RGBA(); alpha == 0 ||
		styles.peerBubble.GetBorderTop() || styles.peerBubble.GetBorderRight() ||
		styles.peerBubble.GetBorderBottom() || styles.peerBubble.GetBorderLeft() {
		t.Fatal("peer message bubble is not a borderless filled surface")
	}
	if _, _, _, alpha := styles.selfBubble.GetBackground().RGBA(); alpha == 0 ||
		styles.selfBubble.GetBorderTop() || styles.selfBubble.GetBorderRight() ||
		styles.selfBubble.GetBorderBottom() || styles.selfBubble.GetBorderLeft() {
		t.Fatal("self message bubble is not a borderless filled surface")
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > model.Width {
			t.Fatalf("bubble view line %d uses %d cells: %q", lineNumber+1, width, ansi.Strip(line))
		}
	}
}

func TestInputSurfaceUsesOneBackgroundAcrossTextareaStyles(t *testing.T) {
	model := NewModel(true)
	want := styles.inputSurface.GetBackground()
	for state, style := range map[string]textarea.Style{
		"focused": model.Input.FocusedStyle,
		"blurred": model.Input.BlurredStyle,
	} {
		for part, got := range map[string]lipgloss.TerminalColor{
			"base": style.Base.GetBackground(), "cursor line": style.CursorLine.GetBackground(),
			"end of buffer": style.EndOfBuffer.GetBackground(), "placeholder": style.Placeholder.GetBackground(),
			"prompt": style.Prompt.GetBackground(), "text": style.Text.GetBackground(),
		} {
			if got != want {
				t.Errorf("%s %s background = %#v, want input surface %#v", state, part, got, want)
			}
		}
	}
	width := 42
	model.Input.SetWidth(width)
	middle := strings.Split(model.inputView(width), "\n")[1]
	if lipgloss.Width(middle) != width {
		t.Fatalf("input middle row width = %d, want %d", lipgloss.Width(middle), width)
	}
	if strings.Contains(ansi.Strip(trimTerminalPadding(model.Input.View())), strings.Repeat(" ", 4)) {
		t.Fatalf("textarea terminal padding was not removed: %q", trimTerminalPadding(model.Input.View()))
	}

	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI256)
	surface := styles.inputSurface.Renderer(renderer)
	// Reproduce the textarea's nested reset followed by already-counted,
	// unstyled viewport padding.
	rendered := fillInputRow("› prompt\x1b[0m          \x1b[0m", 24, surface)
	lastBackground := strings.LastIndex(rendered, "\x1b[48;5;236m")
	contentWidth := ansi.StringWidth(strings.TrimRight(ansi.Strip(rendered), " "))
	if lastBackground < 0 || ansi.StringWidth(rendered[lastBackground:]) != 24-contentWidth {
		t.Fatalf("input row does not color all %d trailing cells: %q", 24-contentWidth, rendered)
	}
}

func TestRoomHeaderSeparatorAndHomeUseSharedLightFrame(t *testing.T) {
	t.Parallel()

	room := NewModel(true)
	room.Page, room.PeerBuilderID = PageRoom, 23
	room.resize(48, 16)
	header := ansi.Strip(room.peerHeaderView(room.Viewport.Width))
	lines := strings.Split(header, "\n")
	if len(lines) != roomHeaderHeight || strings.TrimSpace(lines[len(lines)-1]) != strings.Repeat("─", room.Viewport.Width) {
		t.Fatalf("room header separator is not content-width aligned:\n%s", header)
	}

	home := NewModel(true)
	home.resize(48, 16)
	view := ansi.Strip(home.View())
	if !strings.HasPrefix(view, "╭") || !strings.Contains(view, "Match") || !strings.Contains(view, "Connect") || strings.Contains(view, "idle") {
		t.Fatalf("home does not use the shared framed layout:\n%s", view)
	}
	if _, _, _, alpha := styles.homeSelected.GetBackground().RGBA(); alpha == 0 {
		t.Fatal("home focus state does not use a filled surface")
	}
}

func TestCommandMenuOverlaysConversationWithoutMovingInput(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.BuilderID, model.PeerBuilderID = PageRoom, 1, 2
	model.resize(80, 24)
	closed := normalizedView(model.View())
	closedInputLine := lineContaining(closed, "Press / for commands")
	viewportHeight := model.Viewport.Height

	model.CommandOpen = true
	model.Input.SetValue("/")
	open := normalizedView(model.View())
	if model.Viewport.Height != viewportHeight {
		t.Fatalf("command menu changed viewport height: %d -> %d", viewportHeight, model.Viewport.Height)
	}
	if openInputLine := lastLineContaining(open, "› /"); openInputLine != closedInputLine {
		t.Fatalf("command menu moved input from line %d to %d\nclosed:\n%s\nopen:\n%s",
			closedInputLine, openInputLine, closed, open)
	}
	if !strings.Contains(open, "/next") || !strings.Contains(open, "/exit") || !strings.Contains(open, "/block") {
		t.Fatalf("command overlay is incomplete:\n%s", open)
	}
}

func lineContaining(view, text string) int {
	for index, line := range strings.Split(view, "\n") {
		if strings.Contains(line, text) {
			return index
		}
	}
	return -1
}

func lastLineContaining(view, text string) int {
	result := -1
	for index, line := range strings.Split(view, "\n") {
		if strings.Contains(line, text) {
			result = index
		}
	}
	return result
}

func TestPasteOverInputLimitIsRejectedWithoutChangingDraft(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 80, 24
	model.Input.SetValue("draft")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 64*1024)), Paste: true})
	result := updated.(Model)
	if result.Input.Value() != "draft" || result.InputError != "Input is too large" {
		t.Fatalf("input = %q, error = %q", result.Input.Value(), result.InputError)
	}
}

func TestMultilinePasteRemainsOneDraftUntilEnter(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.Width, model.Height = PageRoom, "room", 80, 24
	model.Input.Focus()
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first\nsecond"), Paste: true})
	result := updated.(Model)
	if result.Input.Value() != "first\nsecond" || len(result.Effects) != 0 {
		t.Fatalf("pasted draft=%q effects=%#v", result.Input.Value(), result.Effects)
	}
	updated, _ = result.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(Model)
	if len(result.Effects) != 1 || result.Effects[0].Type != protocol.EventMessageSend {
		t.Fatalf("pasted message effects=%#v", result.Effects)
	}
}

func TestPasteAboveLineLimitIsRejectedWithoutTruncation(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 80, 24
	model.Input.Focus()
	model.Input.SetValue("draft")
	pasted := strings.Repeat("line\n", textcheck.MaxLines) + "last"
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	result := updated.(Model)
	if result.Input.Value() != "draft" || result.InputError != "Too many lines" {
		t.Fatalf("input=%q error=%q", result.Input.Value(), result.InputError)
	}
}

func TestPasteWithControlCharactersIsRejectedWithoutChangingDraft(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 80, 24
	model.Input.SetValue("draft")
	updated, _ := model.Update(tea.KeyMsg{
		Type: tea.KeyRunes, Runes: []rune("unsafe\rtext"), Paste: true,
	})
	result := updated.(Model)
	if result.Input.Value() != "draft" || result.InputError != "Unsupported text" {
		t.Fatalf("control paste input=%q error=%q", result.Input.Value(), result.InputError)
	}
}

func TestMessageLimitRemovesWholeSenderGroup(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.resize(80, 24)
	model.appendMessage(Message{SenderID: 1, Text: strings.Repeat("a", MaxRoomBytes)})
	model.appendMessage(Message{SenderID: 1, Text: "same group"})
	model.appendMessage(Message{SenderID: 2, Text: "keep"})
	if len(model.Messages) != 1 || model.Messages[0].SenderID != 2 || !model.trimmed {
		t.Fatalf("messages = %#v, trimmed = %v", model.Messages, model.trimmed)
	}
}

func TestCommandMenuFiltersAndKeyboardScrolls(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageRoom, 60, 18
	model.resize(60, 18)
	model.Input.SetValue("/b")
	model.CommandOpen = true
	if view := model.commandView(); !strings.Contains(view, "/block") || strings.Contains(view, "/next") {
		t.Fatalf("filtered command view:\n%s", view)
	} else if lines := strings.Split(ansi.Strip(view), "\n"); len(lines) != commandLayerHeight ||
		strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[1]) != "" ||
		!strings.Contains(lines[2], "/block") {
		t.Fatalf("single filtered command is not bottom-aligned: %#v", lines)
	}
	model.CommandOpen = false
	model.Input.Reset()
	for index := 0; index < 30; index++ {
		model.appendMessage(Message{SenderID: int64(index%2 + 1), Text: "line"})
	}
	model.Viewport.GotoBottom()
	before := model.Viewport.YOffset
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	result := updated.(Model)
	if result.Viewport.YOffset >= before {
		t.Fatalf("page up did not scroll: %d -> %d", before, result.Viewport.YOffset)
	}
}

func TestCommandMenuMovesOneItemCompletesAndExecutesFocusedCommand(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID = PageRoom, "room"
	model.resize(60, 18)
	model.Input.SetValue("/")
	model.CommandOpen = true

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(Model)
	if model.CommandFocus != 2 {
		t.Fatalf("up focus = %d, want 2", model.CommandFocus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.Input.Value() != "/block" || model.CommandOpen {
		t.Fatalf("focused command was not completed: input=%q open=%v", model.Input.Value(), model.CommandOpen)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	effects := model.DrainEffects()
	if len(effects) != 1 || effects[0].Type != protocol.EventRoomBlock {
		t.Fatalf("focused command effects = %#v", effects)
	}
}

func TestMouseWheelScrollsRoomHistory(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageRoom
	model.resize(60, 18)
	for index := 0; index < 30; index++ {
		model.appendMessage(Message{SenderID: int64(index%2 + 1), Text: "line"})
	}
	model.Viewport.GotoBottom()
	before := model.Viewport.YOffset
	updated, _ := model.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress,
	})
	result := updated.(Model)
	if result.Viewport.YOffset >= before {
		t.Fatalf("mouse wheel did not scroll: %d -> %d", before, result.Viewport.YOffset)
	}
}

func TestNewMessageDoesNotForceScrolledViewportToBottom(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageRoom
	model.resize(60, 18)
	for index := 0; index < 30; index++ {
		model.appendMessage(Message{SenderID: int64(index%2 + 1), Text: "line"})
	}
	model.Viewport.GotoTop()
	before := model.Viewport.YOffset
	model.appendMessage(Message{SenderID: 1, Text: "new line"})
	if model.Viewport.AtBottom() || model.Viewport.YOffset != before {
		t.Fatalf("new message moved scrolled viewport: before=%d after=%d bottom=%v",
			before, model.Viewport.YOffset, model.Viewport.AtBottom())
	}
}

func TestAllPagesRenderAtSupportedSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		page Page
		want string
	}{
		{PageAdmission, "Be kind and respectful when chatting."},
		{PageHome, "Connect"},
		{PageWaiting, "Looking for another human"},
		{PageRoom, "Press / for commands"},
		{PageHooks, "No supported agents found."},
		{PageIdentityError, "Create new identity"},
	}
	for _, size := range [][2]int{{48, 16}, {80, 24}, {140, 40}} {
		for _, test := range tests {
			model := NewModel(true)
			model.Page, model.HooksChecked = test.page, true
			model.resize(size[0], size[1])
			view := model.View()
			if !strings.Contains(view, test.want) || strings.Contains(view, "Terminal too small") {
				t.Errorf("page %d at %dx%d:\n%s", test.page, size[0], size[1], view)
			}
			if lines := strings.Count(view, "\n") + 1; lines > size[1] {
				t.Errorf("page %d at %dx%d uses %d lines", test.page, size[0], size[1], lines)
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if width := ansi.StringWidth(line); width > size[0] {
					t.Errorf("page %d at %dx%d line %d uses %d cells: %q",
						test.page, size[0], size[1], lineNumber+1, width, ansi.Strip(line))
				}
			}
		}
	}
}

func TestAdmissionViewShowsConfiguredPublishedDocuments(t *testing.T) {
	t.Parallel()

	model := NewModel(false)
	model.resize(MinWidth, MinHeight)
	model.SetAdmissionLinks("https://lobbyte.online/rules", "https://lobbyte.online/privacy")
	model.Notice = "Could not save acceptance."
	view := model.View()
	for _, expected := range []string{
		"Be kind and respectful when chatting.", "Community Rules", "Privacy Notice",
		"https://lobbyte.online/rules", "https://lobbyte.online/privacy",
		"Could not save acceptance.", "Continue",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("admission view does not contain %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "18+") {
		t.Fatalf("admission view still contains the removed age copy:\n%s", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > model.Height {
		t.Fatalf("published admission view uses %d lines at height %d:\n%s", lines, model.Height, view)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > model.Width {
			t.Fatalf("published admission line %d uses %d cells at width %d: %q",
				lineNumber+1, width, model.Width, ansi.Strip(line))
		}
	}
}

func TestHookResultsUseUnifiedPageAndWrapReasons(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageHooks
	model.SelfPresence = protocol.Presence{State: protocol.AgentWaiting, Source: protocol.SourceCodex}
	model.SetHookResults([]HookResult{
		{Agent: "Codex", Status: "Configured", Reason: "review required"},
		{Agent: "Claude Code", Status: "Disabled", Reason: "could not update config"},
	})
	model.resize(MinWidth, MinHeight)
	view := model.View()
	for _, expected := range []string{
		"╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗", "Codex", "Configured", "(review required)",
		"Claude Code", "Disabled", "(could not update config)",
		"Open /hooks in Codex and trust Lobbyte.", "Back",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("hook results do not contain %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "waiting for Codex") {
		t.Fatalf("unrelated self presence leaked into hook page:\n%s", view)
	}
	assertViewFits(t, model, view)
}

func TestHookConnectingStateUsesStablePageFrame(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.HooksChecked = PageHooks, false
	model.resize(MinWidth, MinHeight)
	view := model.View()
	for _, expected := range []string{"╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗", "Connecting agents…", "Back"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("hook loading page does not contain %q:\n%s", expected, view)
		}
	}
	assertViewFits(t, model, view)
}

func assertViewFits(t *testing.T, model Model, view string) {
	t.Helper()
	if lines := strings.Count(view, "\n") + 1; lines > model.Height {
		t.Fatalf("view uses %d lines at height %d:\n%s", lines, model.Height, view)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > model.Width {
			t.Fatalf("line %d uses %d cells at width %d: %q",
				lineNumber+1, width, model.Width, ansi.Strip(line))
		}
	}
}

func TestSpinnerTickAdvancesWaitingAnimation(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page = PageWaiting
	model.resize(80, 24)
	before := model.Spinner.View()
	updated, command := model.Update(spinner.TickMsg{})
	result := updated.(Model)
	if result.Spinner.View() == before || command == nil {
		t.Fatalf("spinner did not advance: %q -> %q command=%v", before, result.Spinner.View(), command)
	}
}

func TestSpinnerStopsOutsideWaitingPage(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	before := model.Spinner.View()
	updated, command := model.Update(spinner.TickMsg{})
	result := updated.(Model)
	if result.Spinner.View() != before || command != nil {
		t.Fatalf("spinner continued outside waiting: %q -> %q command=%v", before, result.Spinner.View(), command)
	}
}

func TestSpinnerAdvancesWhileHooksAreConnecting(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.HooksChecked = PageHooks, false
	before := model.Spinner.View()
	updated, command := model.Update(spinner.TickMsg{})
	result := updated.(Model)
	if result.Spinner.View() == before || command == nil {
		t.Fatalf("hook spinner did not advance: %q -> %q command=%v", before, result.Spinner.View(), command)
	}
}

func TestIdentityRecoveryPageShowsReplacementFailure(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.Width, model.Height = PageIdentityError, 80, 24
	model.Notice = "Could not create identity."
	if view := model.View(); !strings.Contains(view, model.Notice) {
		t.Fatalf("identity recovery notice is missing:\n%s", view)
	}
}

func TestLatePresenceFromOldRoomIsIgnored(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID = PageRoom, "new-room"
	model.PeerPresence = protocol.Presence{State: protocol.AgentIdle}
	event, _ := protocol.New(protocol.EventPresenceChanged, "presence", protocol.PresenceChanged{
		RoomID: "old-room", Presence: protocol.Presence{State: protocol.AgentWaiting, Source: protocol.SourceCodex},
	})
	if err := model.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	if model.PeerPresence.State != protocol.AgentIdle {
		t.Fatalf("late presence changed new room: %#v", model.PeerPresence)
	}
}

func TestLateRoomEventsCannotPolluteReplacementRoom(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "new-room", 1
	model.PeerBuilderID = 3
	model.appendMessage(Message{SenderID: 3, Text: "new conversation"})
	for _, event := range []protocol.Envelope{
		mustEvent(t, protocol.EventMessageReceived, protocol.MessageReceived{
			RoomID: "old-room", SenderID: 2, Text: "late message",
		}),
		mustEvent(t, protocol.EventRoomEnded, protocol.RoomEnded{
			RoomID: "old-room", InitiatedBy: "peer", Destination: protocol.DestinationWaiting,
		}),
	} {
		if err := model.ApplyRelay(event); err != nil {
			t.Fatal(err)
		}
	}
	if model.Page != PageRoom || model.RoomID != "new-room" || model.PeerBuilderID != 3 ||
		len(model.Messages) != 1 || model.Messages[0].Text != "new conversation" {
		t.Fatalf("replacement room was changed by late events: %#v", model)
	}
}

func TestInvalidRelayRoomContentIsRejectedBeforeRendering(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	for _, event := range []protocol.Envelope{
		mustEvent(t, protocol.EventMessageReceived, protocol.MessageReceived{
			RoomID: "room", SenderID: 2, Text: "\x1b[31munsafe",
		}),
		mustEvent(t, protocol.EventRoomEnded, protocol.RoomEnded{
			RoomID: "room", InitiatedBy: "unknown", Destination: protocol.DestinationWaiting,
		}),
		mustEvent(t, protocol.EventRoomEnded, protocol.RoomEnded{
			RoomID: "room", InitiatedBy: "peer", Destination: protocol.DestinationHome,
		}),
		mustEvent(t, protocol.EventMessageReceived, protocol.MessageReceived{
			RoomID: "room", SenderID: 999, Text: "unknown member",
		}),
		mustEvent(t, protocol.EventError, protocol.Error{
			Code: "operation_failed", Message: "unsafe\x1b[31m",
		}),
	} {
		if err := model.ApplyRelay(event); err == nil {
			t.Fatalf("invalid Relay event %s was accepted", event.Type)
		}
	}
	if model.Page != PageRoom || model.RoomID != "room" || len(model.Messages) != 0 {
		t.Fatalf("invalid Relay content changed the room: %#v", model)
	}

	waiting := NewModel(true)
	waiting.Page = PageWaiting
	invalidJoined := mustEvent(t, protocol.EventRoomJoined, protocol.RoomJoined{
		RoomID: "room", BuilderID: 2,
		Presence: protocol.Presence{State: protocol.AgentIdle, Source: protocol.SourceCodex},
	})
	if err := waiting.ApplyRelay(invalidJoined); err == nil || waiting.Page != PageWaiting {
		t.Fatalf("invalid room join changed waiting state: page=%d err=%v", waiting.Page, err)
	}
	selfJoin := NewModel(true)
	selfJoin.Page, selfJoin.BuilderID = PageWaiting, 2
	if err := selfJoin.ApplyRelay(mustEvent(t, protocol.EventRoomJoined, protocol.RoomJoined{
		RoomID: "room", BuilderID: 2, Presence: protocol.Presence{State: protocol.AgentIdle},
	})); err == nil || selfJoin.Page != PageWaiting {
		t.Fatalf("self room join changed waiting: page=%d err=%v", selfJoin.Page, err)
	}

	unsolicitedJoin := NewModel(true)
	unsolicitedJoin.Page = PageHome
	if err := unsolicitedJoin.ApplyRelay(mustEvent(t, protocol.EventRoomJoined, protocol.RoomJoined{
		RoomID: "room", BuilderID: 2, Presence: protocol.Presence{State: protocol.AgentIdle},
	})); err == nil || unsolicitedJoin.Page != PageHome {
		t.Fatalf("unsolicited room join changed home: page=%d err=%v", unsolicitedJoin.Page, err)
	}
}

func TestMatchingConfirmationIsAcceptedOnlyWhileWaiting(t *testing.T) {
	t.Parallel()

	event := mustEvent(t, protocol.EventMatchWaiting, struct{}{})
	waiting := NewModel(true)
	waiting.Page = PageWaiting
	if err := waiting.ApplyRelay(event); err != nil {
		t.Fatalf("waiting confirmation rejected: %v", err)
	}
	room := NewModel(true)
	room.Page, room.RoomID = PageRoom, "room"
	if err := room.ApplyRelay(event); err == nil || room.Page != PageRoom || room.RoomID != "room" {
		t.Fatalf("out-of-state confirmation: page=%d room=%q err=%v", room.Page, room.RoomID, err)
	}
}

func TestFixedSizeViewSnapshots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		page Page
		set  func(*Model)
		want string
	}{
		{name: "admission", page: PageAdmission, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│            ╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗             │\n" +
			"│            ║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣              │\n" +
			"│            ╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝             │\n" +
			"│                                              │\n" +
			"│    Be kind and respectful when chatting.     │\n" +
			"│  Do not share private code, credentials, or  │\n" +
			"│            personal information.             │\n" +
			"│   Continue accepts the Community Rules and   │\n" +
			"│               Privacy Notice.                │\n" +
			"│                                              │\n" +
			"│  ╭────────────────────────╮  ╭────────────╮  │\n" +
			"│  │        Continue        │  │    Quit    │  │\n" +
			"│  ╰────────────────────────╯  ╰────────────╯  │\n" +
			"│                                              │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "home", page: PageHome, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│                                              │\n" +
			"│            ╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗             │\n" +
			"│            ║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣              │\n" +
			"│            ╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝             │\n" +
			"│                                              │\n" +
			"│  Meet another human while your agent works.  │\n" +
			"│                                              │\n" +
			"│  ╭────────────────────────────────────────╮  │\n" +
			"│  │                 Match                  │  │\n" +
			"│  ╰────────────────────────────────────────╯  │\n" +
			"│  ╭──────────────────╮  ╭──────────────────╮  │\n" +
			"│  │     Connect      │  │       Quit       │  │\n" +
			"│  ╰──────────────────╯  ╰──────────────────╯  │\n" +
			"│                                              │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "waiting", page: PageWaiting, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│                                              │\n" +
			"│            ╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗             │\n" +
			"│            ║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣              │\n" +
			"│            ╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝             │\n" +
			"│                                              │\n" +
			"│  Meet another human while your agent works.  │\n" +
			"│                                              │\n" +
			"│  ╭────────────────────────────────────────╮  │\n" +
			"│  │                  Back                  │  │\n" +
			"│  ╰────────────────────────────────────────╯  │\n" +
			"│                                              │\n" +
			"│        ⣾   Looking for another human…        │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "room", page: PageRoom, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│     (@_                                      │\n" +
			"│  \\\\\\_\\   Builder #0023                       │\n" +
			"│  <____)  waiting for Codex                   │\n" +
			"│  ──────────────────────────────────────────  │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│  › Press / for commands                      │\n" +
			"│                                              │\n" +
			"│                  prompting with Claude Code  │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "command menu", page: PageRoom, set: func(model *Model) {
			model.RoomID, model.CommandOpen = "room", true
			model.Input.SetValue("/")
		}, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│     (@_                                      │\n" +
			"│  \\\\\\_\\   Builder #0023                       │\n" +
			"│  <____)  waiting for Codex                   │\n" +
			"│  ──────────────────────────────────────────  │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│  › /next   switch to another room            │\n" +
			"│    /exit   return to home                    │\n" +
			"│    /block  block and switch rooms            │\n" +
			"│                                              │\n" +
			"│  › /                                         │\n" +
			"│                                              │\n" +
			"│                  prompting with Claude Code  │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "hooks", page: PageHooks, set: func(model *Model) { model.HooksChecked = true }, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│                                              │\n" +
			"│            ╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗             │\n" +
			"│            ║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣              │\n" +
			"│            ╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝             │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│          No supported agents found.          │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│  ╭────────────────────────────────────────╮  │\n" +
			"│  │                  Back                  │  │\n" +
			"│  ╰────────────────────────────────────────╯  │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"╰──────────────────────────────────────────────╯"},
		{name: "identity error", page: PageIdentityError, want: "" +
			"╭──────────────────────────────────────────────╮\n" +
			"│                                              │\n" +
			"│            ╦  ╔═╗╔╗ ╔╗ ╦ ╦╔╦╗╔═╗             │\n" +
			"│            ║  ║ ║╠╩╗╠╩╗╚╦╝ ║ ║╣              │\n" +
			"│            ╩═╝╚═╝╚═╝╚═╝ ╩  ╩ ╚═╝             │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│         Identity could not be read.          │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"│  ╭────────────────────────╮  ╭────────────╮  │\n" +
			"│  │  Create new identity   │  │    Quit    │  │\n" +
			"│  ╰────────────────────────╯  ╰────────────╯  │\n" +
			"│                                              │\n" +
			"│                                              │\n" +
			"╰──────────────────────────────────────────────╯"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(true)
			model.Page = test.page
			model.PeerBuilderID = 23
			model.PeerPresence = protocol.Presence{State: protocol.AgentWaiting, Source: protocol.SourceCodex}
			model.SelfPresence = protocol.Presence{State: protocol.AgentPrompting, Source: protocol.SourceClaudeCode}
			if test.set != nil {
				test.set(&model)
			}
			model.resize(48, 16)
			if got := normalizedView(model.View()); got != test.want {
				t.Fatalf("fixed view changed (-want +got):\nwant:\n%s\n\ngot:\n%s", test.want, got)
			}
		})
	}
}

func TestBuilderIDFormattingUsesFourDigitsThenExpands(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		builderID int64
		want      string
	}{
		{builderID: 23, want: "Builder #0023"},
		{builderID: 12_345, want: "Builder #12345"},
	} {
		model := NewModel(true)
		model.Page, model.PeerBuilderID = PageRoom, test.builderID
		model.resize(80, 24)
		if view := model.View(); !strings.Contains(view, test.want) {
			t.Fatalf("Builder %d view does not contain %q:\n%s", test.builderID, test.want, view)
		}
	}
}

func mustEvent(t *testing.T, eventType protocol.EventType, payload any) protocol.Envelope {
	t.Helper()
	event, err := protocol.New(eventType, "test-event", payload)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func normalizedView(view string) string {
	lines := strings.Split(ansi.Strip(view), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func TestLocalCooldownStartsOnlyAfterBurstRelayAcceptance(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	for index := 0; index < textcheck.BurstMessages; index++ {
		model.Input.SetValue("hello")
		model = model.submitInput()
		if model.InputError != "" {
			t.Fatalf("message %d rejected before Relay: %s", index, model.InputError)
		}
		event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
			RoomID: "room", SenderID: 1, Text: "hello",
		})
		if err := model.ApplyRelay(event); err != nil {
			t.Fatal(err)
		}
	}
	model.Input.SetValue("next")
	model = model.submitInput()
	if model.InputError != "Wait a moment before sending" {
		t.Fatalf("message %d error = %q", textcheck.BurstMessages+1, model.InputError)
	}
}

func TestRelayAcceptanceDoesNotClearANewerDraft(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Input.SetValue("first")
	model = model.submitInput()
	model.Input.SetValue("new draft")
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 1, Text: "first",
	})
	if err := model.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	if model.Input.Value() != "new draft" {
		t.Fatalf("new draft was cleared: %q", model.Input.Value())
	}
}

func TestRelayAcceptanceDoesNotClearDraftEditedBackToSameText(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Input.SetValue("hello")
	model = model.submitInput()
	model.Input.SetValue("other")
	model.inputRevision++
	model.Input.SetValue("hello")
	model.inputRevision++
	event, _ := protocol.New(protocol.EventMessageReceived, "event", protocol.MessageReceived{
		RoomID: "room", SenderID: 1, Text: "hello",
	})
	if err := model.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	if model.Input.Value() != "hello" || model.messagePending {
		t.Fatalf("revised draft or pending state is wrong: input=%q pending=%v",
			model.Input.Value(), model.messagePending)
	}
}

func TestUnrelatedRoomErrorDoesNotReleasePendingMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(true)
	model.Page, model.RoomID, model.BuilderID = PageRoom, "room", 1
	model.Input.SetValue("hello")
	model = model.submitInput()
	event, _ := protocol.New(protocol.EventError, "error", protocol.Error{
		Code: "storage_unavailable", Message: "Relay is temporarily unavailable.", Retryable: true,
	})
	if err := model.ApplyRelay(event); err != nil {
		t.Fatal(err)
	}
	model = model.submitInput()
	if effects := model.DrainEffects(); len(effects) != 1 || model.InputError != "Message is sending" {
		// The one effect is the original send; the second submission must add none.
		t.Fatalf("unrelated error released message: effects=%#v error=%q", effects, model.InputError)
	}
}
