package top

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
)

// Screen is the narrow terminal contract used by App. A tcell.Screen satisfies
// it directly, while tests can provide a deterministic fake.
type Screen interface {
	Init() error
	Fini()
	Clear()
	SetStyle(tcell.Style)
	HideCursor()
	Size() (int, int)
	EventQ() chan tcell.Event
	Put(int, int, string, tcell.Style) (string, int)
	Show()
	Sync()
}

// ScreenFactory creates a terminal screen.
type ScreenFactory func() (Screen, error)

// NewTCellScreen creates the production tcell screen.
func NewTCellScreen() (Screen, error) { return tcell.NewScreen() }

// ErrScreenClosed reports an unexpected terminal event queue shutdown.
var ErrScreenClosed = errors.New("terminal event queue closed")

// App owns terminal initialization, input, rendering, and restoration. Events
// may be nil. OnEffect must return quickly and must not call Screen methods.
type App struct {
	Initial      State
	Events       <-chan Event
	Factory      ScreenFactory
	Theme        Theme
	Glyphs       Glyphs
	TickInterval time.Duration
	OnEffect     func(Effect)
}

// Run blocks until quit, interruption, context cancellation, or terminal
// failure. Fini is guaranteed after successful Init, including during panics.
func (app App) Run(ctx context.Context) (final State, err error) {
	factory := app.Factory
	if factory == nil {
		factory = NewTCellScreen
	}
	screen, err := factory()
	if err != nil {
		return app.Initial, fmt.Errorf("create terminal screen: %w", err)
	}
	if err := screen.Init(); err != nil {
		return app.Initial, fmt.Errorf("initialize terminal screen: %w", err)
	}
	defer func() {
		screen.Fini()
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
	}()

	state := app.Initial
	if state.Revision == 0 {
		state = NewState(state.Snapshot)
	}
	width, height := screen.Size()
	state, _ = Update(state, ResizeEvent{Width: width, Height: height})
	screen.SetStyle(styleFor(app.Theme, RoleDefault))
	screen.HideCursor()
	drawScreen(screen, Render(state, app.Glyphs), app.Theme, false)

	interval := app.TickInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	events := app.Events
	screenEvents := screen.EventQ()

	for {
		select {
		case <-ctx.Done():
			if state.Snapshot.Live.Active && !state.Snapshot.Live.Completed && app.OnEffect != nil {
				app.OnEffect(Effect{Kind: EffectCancelRun})
			}
			return state, ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			next, effects := Update(state, event)
			quit, redraw := app.performEffects(effects)
			if quit {
				return next, nil
			}
			if next.Revision != state.Revision || redraw {
				drawScreen(screen, Render(next, app.Glyphs), app.Theme, redraw)
			}
			state = next
		case event, ok := <-screenEvents:
			if !ok {
				return state, ErrScreenClosed
			}
			if terminalError, ok := event.(*tcell.EventError); ok {
				return state, fmt.Errorf("terminal input: %w", terminalError)
			}
			semantic, accepted := mapTCellEvent(event, state)
			if !accepted {
				continue
			}
			next, effects := Update(state, semantic)
			quit, redraw := app.performEffects(effects)
			_, resized := event.(*tcell.EventResize)
			if quit {
				return next, nil
			}
			if next.Revision != state.Revision || redraw || resized {
				drawScreen(screen, Render(next, app.Glyphs), app.Theme, redraw || resized)
			}
			state = next
		case now := <-ticker.C:
			if !state.Snapshot.Live.Active || state.Snapshot.Live.Completed || state.Paused {
				continue
			}
			next, _ := Update(state, TickEvent{Now: now})
			if next.Revision != state.Revision {
				drawScreen(screen, Render(next, app.Glyphs), app.Theme, false)
			}
			state = next
		}
	}
}

func (app App) performEffects(effects []Effect) (quit, redraw bool) {
	for _, effect := range effects {
		switch effect.Kind {
		case EffectQuit:
			quit = true
		case EffectRedraw:
			redraw = true
		default:
			if app.OnEffect != nil {
				app.OnEffect(effect)
			}
		}
	}
	return quit, redraw
}

func mapTCellEvent(event tcell.Event, state State) (Event, bool) { //nolint:gocyclo
	switch value := event.(type) {
	case *tcell.EventInterrupt:
		return InputEvent{Action: ActionInterrupt}, true
	case *tcell.EventResize:
		width, height := value.Size()
		return ResizeEvent{Width: width, Height: height}, true
	case *tcell.EventKey:
		if !value.Pressed() {
			return nil, false
		}
		key, modifiers := value.Key(), value.Modifiers()
		if key == tcell.KeyCtrlC || (key == tcell.KeyRune && modifiers&tcell.ModCtrl != 0 && value.Str() == "c") {
			return InputEvent{Action: ActionInterrupt}, true
		}
		if key == tcell.KeyCtrlL || (key == tcell.KeyRune && modifiers&tcell.ModCtrl != 0 && value.Str() == "l") {
			return InputEvent{Action: ActionRedraw}, true
		}
		if key == tcell.KeyCtrlU && state.EditingFilter {
			return InputEvent{Action: ActionClearFilter}, true
		}
		if state.ConfirmQuit {
			switch value.Str() {
			case "y", "Y":
				return InputEvent{Action: ActionConfirm}, true
			case "n", "N":
				return InputEvent{Action: ActionReject}, true
			}
		}
		switch key {
		case tcell.KeyEscape:
			return InputEvent{Action: ActionBack}, true
		case tcell.KeyEnter:
			return InputEvent{Action: ActionOpen}, true
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			return InputEvent{Action: ActionBackspace}, true
		case tcell.KeyTab:
			return InputEvent{Action: ActionNextView}, true
		case tcell.KeyBacktab:
			return InputEvent{Action: ActionPrevView}, true
		case tcell.KeyUp:
			return InputEvent{Action: ActionUp}, true
		case tcell.KeyDown:
			return InputEvent{Action: ActionDown}, true
		case tcell.KeyLeft:
			return InputEvent{Action: ActionPrevView}, true
		case tcell.KeyRight:
			return InputEvent{Action: ActionNextView}, true
		case tcell.KeyPgUp:
			return InputEvent{Action: ActionPageUp}, true
		case tcell.KeyPgDn:
			return InputEvent{Action: ActionPageDown}, true
		case tcell.KeyHome:
			return InputEvent{Action: ActionHome}, true
		case tcell.KeyEnd:
			return InputEvent{Action: ActionEnd}, true
		case tcell.KeyF1:
			return InputEvent{Action: ActionHelp}, true
		}
		if key != tcell.KeyRune {
			return nil, false
		}
		if modifiers&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) != 0 {
			return nil, false
		}
		text := value.Str()
		if state.EditingFilter {
			return InputEvent{Action: ActionText, Text: text}, text != ""
		}
		switch text {
		case "1":
			return InputEvent{Action: ActionViewLive}, true
		case "2":
			return InputEvent{Action: ActionViewResult}, true
		case "3":
			return InputEvent{Action: ActionViewBoard}, true
		case "4":
			return InputEvent{Action: ActionViewHistory}, true
		case "5":
			return InputEvent{Action: ActionViewInventory}, true
		case "j", "J":
			return InputEvent{Action: ActionDown}, true
		case "k", "K":
			return InputEvent{Action: ActionUp}, true
		case "h", "H":
			return InputEvent{Action: ActionPrevView}, true
		case "l", "L":
			return InputEvent{Action: ActionNextView}, true
		case "g":
			return InputEvent{Action: ActionHome}, true
		case "G":
			return InputEvent{Action: ActionEnd}, true
		case "/":
			return InputEvent{Action: ActionFilter}, true
		case "s", "S":
			return InputEvent{Action: ActionSort}, true
		case "c", "C":
			return InputEvent{Action: ActionCompare}, true
		case " ":
			return InputEvent{Action: ActionPause}, true
		case "r", "R":
			return InputEvent{Action: ActionReload}, true
		case "?":
			return InputEvent{Action: ActionHelp}, true
		case "q", "Q":
			return InputEvent{Action: ActionQuit}, true
		case "y", "Y":
			return InputEvent{Action: ActionConfirm}, true
		case "n", "N":
			return InputEvent{Action: ActionReject}, true
		}
	}
	return nil, false
}

func drawScreen(screen Screen, canvas Canvas, theme Theme, sync bool) {
	screen.Clear()
	screen.HideCursor()
	for y, row := range canvas.Rows {
		x := 0
		for _, span := range row {
			remaining := span.Text
			style := styleFor(theme, span.Role)
			for remaining != "" && x < canvas.Width {
				next, width := screen.Put(x, y, remaining, style)
				if width <= 0 || next == remaining {
					break
				}
				x += width
				remaining = next
			}
		}
	}
	if sync {
		screen.Sync()
	} else {
		screen.Show()
	}
}

func styleFor(theme Theme, role Role) tcell.Style {
	if int(role) >= len(theme.Styles) {
		role = RoleDefault
	}
	spec := theme.Styles[role]
	style := tcell.StyleDefault.Foreground(tcellColor(spec.Foreground))
	style = style.Bold(spec.Bold).Dim(spec.Dim).Reverse(spec.Reverse)
	return style
}

func tcellColor(value Color) tcell.Color {
	switch value {
	case ColorRed:
		return color.Red
	case ColorGreen:
		return color.Green
	case ColorYellow:
		return color.Yellow
	case ColorBlue:
		return color.Blue
	case ColorMagenta:
		return color.Purple
	case ColorCyan:
		return color.Teal
	case ColorGray:
		return color.Gray
	default:
		return color.Default
	}
}
