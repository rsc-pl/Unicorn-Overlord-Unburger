package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/build"
	"uo-unburger/internal/paths"
)

// taskProgressMsg carries a measured stage update from a background task.
type taskProgressMsg struct {
	id    int
	stage string
	done  int
	total int
}

// taskDoneMsg carries a finished task's report text, an optional structured
// result for the originating screen, or its error.
type taskDoneMsg struct {
	id     int
	report string
	data   any
	err    error
}

var taskSeq int64

// taskChoice is one selectable answer to a running task's question.
type taskChoice struct {
	// Key is the single-character accelerator the operator presses.
	Key string
	// Label is the human wording shown next to the key.
	Label string
	// Value is what the task receives when this choice is taken.
	Value string
}

// taskQuestion is a decision a running task needs from the operator. The task
// blocks until the operator answers it or the task is cancelled.
type taskQuestion struct {
	Title   string
	Detail  string
	Choices []taskChoice
}

// taskAskMsg asks the interface to present a question for a still-running task.
type taskAskMsg struct {
	id       int
	question taskQuestion
}

// progressFunc reports a measured stage counter for a running operation. It is
// the named form of the progress callback so a signature that uses it stays
// readable.
type progressFunc func(stage string, done, total int)

// askFunc asks the interface a question for a running operation and returns the
// chosen value, or an error when the operation was cancelled first.
type askFunc func(q taskQuestion) (string, error)

// taskFunc is a background operation that reports progress.
type taskFunc func(ctx context.Context, prog func(stage string, done, total int)) (string, any, error)

// taskAskFunc is a background operation that may ask the operator a question.
type taskAskFunc func(ctx context.Context, prog progressFunc, ask askFunc) (string, any, error)

// task is one background operation. Progress values are real counters reported
// by the operation itself (build stages, self-check checks); the UI never invents
// a percentage for work it cannot measure.
type task struct {
	id        int
	label     string
	stage     string
	done      int
	total     int
	started   time.Time
	cancel    context.CancelFunc
	ch        chan tea.Msg
	answer    chan string
	doneFlag  bool
	cancelled bool
	report    string
	err       error
}

// startTask launches fn in the background and returns the command that waits for
// its first message. Only one task may run at a time; a second request is
// refused so two writers can never race over the same output.
//
// fn runs on its own goroutine and therefore must not touch the model: it
// returns a report string plus an optional structured result, which the message
// loop applies on the UI goroutine.
func (m *Model) startTask(label string, fn taskFunc) tea.Cmd {
	return m.startTaskAsk(label, func(ctx context.Context, prog progressFunc, _ askFunc) (string, any, error) {
		return fn(ctx, prog)
	})
}

// startTaskAsk is startTask for an operation that may need a decision from the
// operator while it runs. The supplied ask function sends a question to the
// interface and blocks until the answer arrives or the context is cancelled, so
// no file is written until the operator has chosen.
func (m *Model) startTaskAsk(label string, fn taskAskFunc) tea.Cmd {
	if m.task != nil && !m.task.doneFlag {
		m.setStatus("a task is already running", true)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	id := int(atomic.AddInt64(&taskSeq, 1))
	ch := make(chan tea.Msg, 64)
	t := &task{id: id, label: label, cancel: cancel, ch: ch, answer: make(chan string, 1), started: time.Now(), total: 0}
	m.task = t
	go func() {
		defer close(ch)
		prog := func(stage string, done, total int) {
			select {
			case ch <- taskProgressMsg{id: id, stage: stage, done: done, total: total}:
			case <-ctx.Done():
			}
		}
		ask := func(q taskQuestion) (string, error) {
			select {
			case ch <- taskAskMsg{id: id, question: q}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case v := <-t.answer:
				return v, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		report, data, err := fn(ctx, prog, ask)
		ch <- taskDoneMsg{id: id, report: report, data: data, err: err}
	}()
	return waitForTask(ch)
}

// waitForTask reads the next message from a task channel.
func waitForTask(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// presentTaskQuestion shows a running task's question as a modal choice dialog.
// The chosen value is delivered to the task through its answer channel, and the
// returned command resumes waiting on the task. Escape takes the last choice
// (the caller lists Cancel last), so a dismissed dialog still unblocks the task
// safely.
func (m *Model) presentTaskQuestion(q taskQuestion) {
	t := m.task
	if t == nil {
		return
	}
	choices := make([]confirmChoice, 0, len(q.Choices))
	for _, c := range q.Choices {
		choices = append(choices, confirmChoice{key: c.Key, label: c.Label, value: c.Value})
	}
	answer := func(value string) tea.Cmd {
		if t == m.task && !t.doneFlag {
			t.answer <- value
		}
		return waitForTask(t.ch)
	}
	cancelValue := "cancel"
	if len(choices) > 0 {
		cancelValue = choices[len(choices)-1].value
	}
	m.confirm = &confirmState{
		title:   q.Title,
		detail:  q.Detail,
		choices: choices,
		pick:    answer,
		cancel:  func() tea.Cmd { return answer(cancelValue) },
	}
}

// cancelTask requests cancellation of the running task. The operation stops at
// its next stage boundary and leaves no partial output because builds publish
// only in their final stage.
func (m *Model) cancelTask() {
	if m.task != nil && !m.task.doneFlag && m.task.cancel != nil {
		m.task.cancel()
		m.task.cancelled = true
	}
}

// running reports whether a task is currently executing.
func (m *Model) running() bool { return m.task != nil && !m.task.doneFlag }

// progressLine renders a measured progress line for the running task.
func (m *Model) progressLine() string {
	if m.task == nil {
		return ""
	}
	t := m.task
	if t.doneFlag {
		return ""
	}
	spin := ""
	if m.theme == nil || !m.theme.NoColor {
		spin = m.spin.View() + " "
	}
	stage := t.stage
	if t.cancelled {
		stage = "cancelling… " + stage
	}
	if t.total <= 0 {
		return strings.TrimSpace(spin + t.label + " — " + stage)
	}
	width := 24
	filled := 0
	if t.total > 0 {
		filled = t.done * width / t.total
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	return fmt.Sprintf("%s%s  [%s] %d/%d stages — %s", spin, t.label, bar, t.done, t.total, stage)
}

// finishTaskMessage renders the completion line for the last finished task.
func (m *Model) finishTaskMessage() (string, bool) {
	if m.task == nil || !m.task.doneFlag {
		return "", false
	}
	t := m.task
	took := time.Since(t.started).Round(time.Millisecond)
	if t.err != nil {
		if t.cancelled || isCancelled(t.err) {
			return fmt.Sprintf("%s: cancelled after %s (no output written)", t.label, took), true
		}
		return fmt.Sprintf("%s failed after %s: %s", t.label, took, paths.Error(t.err)), true
	}
	return fmt.Sprintf("%s finished in %s", t.label, took), false
}

func isCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, build.ErrOutputCancelled)
}
