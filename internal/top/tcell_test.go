package top

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v3"
)

type fakeScreen struct {
	mu                    sync.Mutex
	events                chan tcell.Event
	width, height         int
	initErr               error
	initialized, finished bool
	shows, syncs, clears  int
	content               map[[2]int]string
}

func newFakeScreen(width, height int) *fakeScreen {
	return &fakeScreen{events: make(chan tcell.Event, 32), width: width, height: height,
		content: map[[2]int]string{}}
}

func (f *fakeScreen) Init() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.initErr != nil {
		return f.initErr
	}
	f.initialized = true
	return nil
}
func (f *fakeScreen) Fini() { f.mu.Lock(); f.finished = true; f.mu.Unlock() }
func (f *fakeScreen) Clear() {
	f.mu.Lock()
	f.clears++
	f.content = map[[2]int]string{}
	f.mu.Unlock()
}
func (f *fakeScreen) SetStyle(tcell.Style)     {}
func (f *fakeScreen) HideCursor()              {}
func (f *fakeScreen) Size() (int, int)         { return f.width, f.height }
func (f *fakeScreen) EventQ() chan tcell.Event { return f.events }
func (f *fakeScreen) Put(x, y int, value string, _ tcell.Style) (string, int) {
	r, size := utf8.DecodeRuneInString(value)
	if size == 0 {
		return "", 0
	}
	f.mu.Lock()
	f.content[[2]int{x, y}] = string(r)
	f.mu.Unlock()
	return value[size:], runeWidth(r)
}
func (f *fakeScreen) Show() { f.mu.Lock(); f.shows++; f.mu.Unlock() }
func (f *fakeScreen) Sync() { f.mu.Lock(); f.syncs++; f.mu.Unlock() }

func TestAppLifecycleQuitAndResize(t *testing.T) {
	screen := newFakeScreen(80, 24)
	screen.events <- tcell.NewEventResize(100, 30)
	screen.events <- tcell.NewEventKey(tcell.KeyRune, "q", 0)
	app := App{Initial: NewState(testSnapshot()), Factory: func() (Screen, error) { return screen, nil },
		Theme: DefaultTheme(false), Glyphs: DefaultGlyphs(false)}
	final, err := app.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !screen.initialized || !screen.finished {
		t.Fatalf("lifecycle init=%v fini=%v", screen.initialized, screen.finished)
	}
	if final.Width != 100 || final.Height != 30 || screen.syncs == 0 || screen.shows == 0 {
		t.Fatalf("resize state=%dx%d syncs=%d shows=%d", final.Width, final.Height, screen.syncs, screen.shows)
	}
}

func TestAppInterruptCancelsActiveRun(t *testing.T) {
	screen := newFakeScreen(80, 24)
	snapshot := testSnapshot()
	snapshot.Live = Live{Active: true, Model: "running", StartedAt: time.Now()}
	screen.events <- tcell.NewEventKey(tcell.KeyCtrlC, "", 0)
	var effects []Effect
	app := App{Initial: NewState(snapshot), Factory: func() (Screen, error) { return screen, nil },
		Theme: DefaultTheme(true), Glyphs: DefaultGlyphs(true), OnEffect: func(effect Effect) { effects = append(effects, effect) }}
	final, err := app.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !final.Interrupted || len(effects) != 1 || effects[0].Kind != EffectCancelRun || !screen.finished {
		t.Fatalf("final=%+v effects=%+v finished=%v", final, effects, screen.finished)
	}
}

func TestAppRestoresTerminalAfterPanic(t *testing.T) {
	screen := newFakeScreen(80, 24)
	screen.events <- tcell.NewEventKey(tcell.KeyRune, "r", 0)
	app := App{Initial: NewState(testSnapshot()), Factory: func() (Screen, error) { return screen, nil },
		Theme: DefaultTheme(false), Glyphs: DefaultGlyphs(false), OnEffect: func(Effect) { panic("boom") }}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), "boom") {
				t.Fatalf("panic = %v", recovered)
			}
		}()
		_, _ = app.Run(context.Background())
	}()
	if !screen.finished {
		t.Fatal("screen was not finalized before panic propagated")
	}
}

func TestAppInitErrorDoesNotFinalizeUninitializedScreen(t *testing.T) {
	screen := newFakeScreen(80, 24)
	screen.initErr = errors.New("no terminal")
	app := App{Initial: NewState(testSnapshot()), Factory: func() (Screen, error) { return screen, nil }}
	_, err := app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("error = %v", err)
	}
	if screen.finished {
		t.Fatal("Fini called after failed Init")
	}
}

func TestAppDoesNotRedrawWhileIdle(t *testing.T) {
	screen := newFakeScreen(80, 24)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	app := App{Initial: NewState(testSnapshot()), Factory: func() (Screen, error) { return screen, nil },
		Theme: DefaultTheme(false), Glyphs: DefaultGlyphs(false), TickInterval: time.Millisecond}
	_, err := app.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if screen.shows != 1 {
		t.Fatalf("idle app rendered %d times, want one initial draw", screen.shows)
	}
}

func TestDocumentedKeyMappings(t *testing.T) {
	tests := []struct {
		event  tcell.Event
		state  State
		action Action
	}{
		{tcell.NewEventKey(tcell.KeyRune, "c", 0), State{}, ActionCompare},
		{tcell.NewEventKey(tcell.KeyRune, "g", 0), State{}, ActionHome},
		{tcell.NewEventKey(tcell.KeyRune, "G", 0), State{}, ActionEnd},
		{tcell.NewEventKey(tcell.KeyRune, "h", 0), State{}, ActionPrevView},
		{tcell.NewEventKey(tcell.KeyRune, "l", 0), State{}, ActionNextView},
		{tcell.NewEventKey(tcell.KeyF1, "", 0), State{}, ActionHelp},
		{tcell.NewEventKey(tcell.KeyCtrlU, "", 0), State{EditingFilter: true}, ActionClearFilter},
	}
	for _, test := range tests {
		event, ok := mapTCellEvent(test.event, test.state)
		input, inputOK := event.(InputEvent)
		if !ok || !inputOK || input.Action != test.action {
			t.Errorf("event=%T mapped=%+v accepted=%v, want %v", test.event, event, ok, test.action)
		}
	}
}
