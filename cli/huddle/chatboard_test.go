package huddle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
	"github.com/stretchr/testify/require"
)

// tcellEnter is the key handleDone treats as "send".
const tcellEnter = tcell.KeyEnter

// fakeChat stands in for the relay connection, so the board can be driven with
// no network and no signing.
type fakeChat struct {
	incoming chan *nip01.Event

	mu     sync.Mutex
	sent   []sentMessage
	closes int
	err    error
}

type sentMessage struct {
	content string
	parent  string
	quotes  []string
}

func newFakeChat() *fakeChat {
	return &fakeChat{incoming: make(chan *nip01.Event, 8)}
}

func (f *fakeChat) messages() <-chan *nip01.Event { return f.incoming }
func (f *fakeChat) self() string                  { return selfPub }

func (f *fakeChat) send(_ context.Context, content, parent string, quotes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMessage{content: content, parent: parent, quotes: quotes})
	return f.err
}

func (f *fakeChat) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
}

func (f *fakeChat) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeChat) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// Without chat the board is exactly what it was: one focusable panel.
func TestBoard_WithoutChatHasOnlyTheRoster(t *testing.T) {
	board := NewBoard(nil, newFakeClient(), "standup")
	require.Len(t, board.Childs(), 1)
}

// With chat the transcript and the composer join Tab's cycle.
func TestBoard_EnableChatAddsTwoFocusablePanels(t *testing.T) {
	board := NewBoard(nil, newFakeClient(), "standup")
	board.EnableChat(chatActivity, newFakeChat())

	require.Len(t, board.Childs(), 3)
}

func TestBoard_RunRecordsIncomingChatMessages(t *testing.T) {
	client := newFakeClient()
	chat := newFakeChat()
	board := NewBoard(nil, client, "standup")
	board.EnableChat(chatActivity, chat)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		board.Run(ctx)
		close(done)
	}()

	chat.incoming <- chatEvent("a", peerPub, "hello", 100, "")

	require.Eventually(t, func() bool { return board.chat.log.len() == 1 },
		2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

// A dropped relay connection closes the message channel. The call itself is
// unaffected, so the board must keep running rather than treating it as the
// end of the huddle.
func TestBoard_SurvivesTheChatChannelClosing(t *testing.T) {
	client := newFakeClient()
	chat := newFakeChat()
	board := NewBoard(nil, client, "standup")
	board.EnableChat(chatActivity, chat)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		board.Run(ctx)
		close(done)
	}()

	close(chat.incoming)

	// The roster keeps ticking, which is how we know Run did not return.
	client.setRoster(client.Self())
	require.Never(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 300*time.Millisecond, 50*time.Millisecond)

	cancel()
	<-done
}

// Leaving the call has to take the relay connection with it, or the process
// would hold a socket open after the board is gone.
func TestBoard_LeaveClosesTheChatConnection(t *testing.T) {
	chat := newFakeChat()
	board := NewBoard(nil, newFakeClient(), "standup")
	board.EnableChat(chatActivity, chat)

	board.Leave()
	require.Equal(t, 1, chat.closeCount())

	// Leave is reachable from both the confirm dialog and a Run-loop
	// shutdown, so it must stay idempotent.
	board.Leave()
	require.Equal(t, 1, chat.closeCount())
}

// A message for a different activity must not appear in this conversation,
// even if a relay sends it.
func TestBoard_IgnoresChatForAnotherActivity(t *testing.T) {
	chat := newFakeChat()
	board := NewBoard(nil, newFakeClient(), "standup")
	board.EnableChat(chatActivity, chat)

	foreign := chatEvent("x", peerPub, "elsewhere", 100, "")
	foreign.Tags = [][]string{{"a", "30312:" + peerPub + ":other"}}

	require.False(t, board.chat.log.add(foreign))
	require.Zero(t, board.chat.log.len())
}

// The composer's reply state is what turns a message into a threaded reply, so
// it has to reach the sender.
func TestChatPanel_SendsAReplyWithItsParent(t *testing.T) {
	chat := newFakeChat()
	log := newChatLog(chatActivity)
	require.True(t, log.add(chatEvent("root", peerPub, "question", 100, "")))

	panel := newChatPanel(nil, log, chat)
	panel.replying, panel.replyTo = true, chatMessage{ID: "root", Author: peerPub}
	panel.input.SetText("an answer")
	panel.handleDone(tcellEnter)

	require.Eventually(t, func() bool { return len(chat.sentMessages()) == 1 },
		2*time.Second, 10*time.Millisecond)

	sent := chat.sentMessages()[0]
	require.Equal(t, "an answer", sent.content)
	require.Equal(t, "root", sent.parent, "the reply must name the message it answers")
}

func TestChatPanel_SendsQuotes(t *testing.T) {
	chat := newFakeChat()
	panel := newChatPanel(nil, newChatLog(chatActivity), chat)

	panel.addQuote(chatMessage{ID: "quoted"})
	panel.addQuote(chatMessage{ID: "quoted"}) // deduped
	panel.input.SetText("look at this")
	panel.handleDone(tcellEnter)

	require.Eventually(t, func() bool { return len(chat.sentMessages()) == 1 },
		2*time.Second, 10*time.Millisecond)

	require.Equal(t, []string{"quoted"}, chat.sentMessages()[0].quotes)
}

// Enter on an empty composer must not publish an empty message.
func TestChatPanel_DoesNotSendBlankMessages(t *testing.T) {
	chat := newFakeChat()
	panel := newChatPanel(nil, newChatLog(chatActivity), chat)

	panel.input.SetText("   ")
	panel.handleDone(tcellEnter)

	require.Never(t, func() bool { return len(chat.sentMessages()) > 0 },
		200*time.Millisecond, 50*time.Millisecond)
}

// Composing state is cleared once sent, so the next message is not silently
// also a reply to the same parent.
func TestChatPanel_ClearsComposingStateAfterSending(t *testing.T) {
	chat := newFakeChat()
	panel := newChatPanel(nil, newChatLog(chatActivity), chat)

	panel.replying, panel.replyTo = true, chatMessage{ID: "root"}
	panel.addQuote(chatMessage{ID: "quoted"})
	panel.input.SetText("first")
	panel.handleDone(tcellEnter)

	require.Eventually(t, func() bool { return len(chat.sentMessages()) == 1 },
		2*time.Second, 10*time.Millisecond)

	panel.input.SetText("second")
	panel.handleDone(tcellEnter)

	require.Eventually(t, func() bool { return len(chat.sentMessages()) == 2 },
		2*time.Second, 10*time.Millisecond)

	second := chat.sentMessages()[1]
	require.Empty(t, second.parent, "the second message must not inherit the reply")
	require.Empty(t, second.quotes, "the second message must not inherit the quotes")
}
