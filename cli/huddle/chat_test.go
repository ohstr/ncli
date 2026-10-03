package huddle

import (
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/stretchr/testify/require"
)

const chatActivity = "30312:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:standup"

// chatEvent builds a kind:1311 event the way a peer would. id is set by hand
// because nothing here signs, and the log keys on it.
func chatEvent(id, author, content string, createdAt uint64, parent string, quotes ...string) *nip01.Event {
	ev := nip53.NewLiveChatMessage(nip53.LiveChatMessageParams{
		Pubkey:   author,
		Activity: chatActivity,
		Parent:   parent,
		Quotes:   quotes,
		Content:  content,
	})
	ev.ID = id
	ev.CreatedAt = createdAt
	return ev
}

func contents(lines []chatLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Content)
	}
	return out
}

func depths(lines []chatLine) []int {
	out := make([]int, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Depth)
	}
	return out
}

func TestChatLog_OrdersRootsOldestFirst(t *testing.T) {
	log := newChatLog(chatActivity)
	require.True(t, log.add(chatEvent("c", "author", "third", 300, "")))
	require.True(t, log.add(chatEvent("a", "author", "first", 100, "")))
	require.True(t, log.add(chatEvent("b", "author", "second", 200, "")))

	require.Equal(t, []string{"first", "second", "third"}, contents(log.lines()))
}

// Equal created_at is common at one-second resolution, so the tiebreak has to
// be deterministic or the view reshuffles on every draw.
func TestChatLog_BreaksTimestampTiesByID(t *testing.T) {
	log := newChatLog(chatActivity)
	require.True(t, log.add(chatEvent("bbb", "author", "b", 100, "")))
	require.True(t, log.add(chatEvent("aaa", "author", "a", 100, "")))

	require.Equal(t, []string{"a", "b"}, contents(log.lines()))
	require.Equal(t, contents(log.lines()), contents(log.lines()), "order must be stable across calls")
}

func TestChatLog_NestsRepliesUnderTheirParent(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("root", "author", "question", 100, ""))
	log.add(chatEvent("kid", "other", "answer", 200, "root"))
	log.add(chatEvent("grandkid", "author", "thanks", 300, "kid"))
	log.add(chatEvent("root2", "author", "new topic", 400, ""))

	lines := log.lines()
	require.Equal(t, []string{"question", "answer", "thanks", "new topic"}, contents(lines))
	require.Equal(t, []int{0, 1, 2, 0}, depths(lines))
	require.False(t, lines[1].Orphan)
}

// Relays deliver stored events in no guaranteed order, so a reply routinely
// arrives before the message it answers. It must re-nest once the parent shows
// up rather than staying stranded at the root.
func TestChatLog_ReplyArrivingBeforeItsParentRenestsLater(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("kid", "other", "answer", 200, "root"))

	lines := log.lines()
	require.Equal(t, []int{0}, depths(lines))
	require.True(t, lines[0].Orphan, "its parent is not here, and the view must be able to say so")

	log.add(chatEvent("root", "author", "question", 100, ""))

	lines = log.lines()
	require.Equal(t, []string{"question", "answer"}, contents(lines))
	require.Equal(t, []int{0, 1}, depths(lines))
	require.False(t, lines[1].Orphan)
}

// Indentation past a few levels eats the message in a terminal, but a capped
// message is still shown -- nothing is dropped for nesting too deep.
func TestChatLog_CapsIndentationWithoutHidingMessages(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("m0", "author", "m0", 100, ""))
	for i := 1; i <= maxChatDepth+3; i++ {
		log.add(chatEvent(
			idForDepth(i), "author", idForDepth(i), uint64(100+i), idForDepth(i-1)))
	}

	lines := log.lines()
	require.Len(t, lines, maxChatDepth+4, "every message must still be displayed")
	for _, l := range lines {
		require.LessOrEqual(t, l.Depth, maxChatDepth)
	}
	require.Equal(t, maxChatDepth, lines[len(lines)-1].Depth)
}

func idForDepth(i int) string {
	return string(rune('m')) + string(rune('0'+i))
}

// `e` tags come from other peers, so a cycle is something they can author. It
// must cost a dropped nesting, not a hung terminal or a lost message.
func TestChatLog_SurvivesAReplyCycle(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("a", "author", "a", 100, "b"))
	log.add(chatEvent("b", "author", "b", 200, "a"))

	lines := log.lines()
	require.Len(t, lines, 2, "both messages must appear despite the cycle")
	require.ElementsMatch(t, []string{"a", "b"}, contents(lines))
}

func TestChatLog_SurvivesASelfReply(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("a", "author", "a", 100, "a"))

	lines := log.lines()
	require.Len(t, lines, 1)
	require.Equal(t, 0, lines[0].Depth)
}

// A relay may answer a broad filter with more than was asked for, and a
// message tagged for another meeting is not part of this conversation.
func TestChatLog_RejectsMessagesForAnotherActivity(t *testing.T) {
	log := newChatLog(chatActivity)
	other := chatEvent("x", "author", "elsewhere", 100, "")
	other.Tags = [][]string{{"a", "30312:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:other"}}

	require.False(t, log.add(other))
	require.Zero(t, log.len())
}

func TestChatLog_RejectsDuplicatesAndJunk(t *testing.T) {
	log := newChatLog(chatActivity)
	first := chatEvent("a", "author", "hello", 100, "")

	require.True(t, log.add(first))
	require.False(t, log.add(first), "the same event id must not appear twice")
	require.False(t, log.add(nil))
	require.False(t, log.add(&nip01.Event{Kind: 1, ID: "b"}), "wrong kind")
	// kind:1311 with no "a" tag fails nip53 parsing, which is the spec's MUST.
	require.False(t, log.add(&nip01.Event{Kind: nip53.KindLiveChatMessage, ID: "c"}))
	// An event with no id cannot be deduplicated or replied to.
	require.False(t, log.add(chatEvent("", "author", "anonymous", 100, "")))

	require.Equal(t, 1, log.len())
}

func TestChatLog_KeepsQuotes(t *testing.T) {
	log := newChatLog(chatActivity)
	require.True(t, log.add(chatEvent("a", "author", "see this", 100, "", "quoted-1", "quoted-2")))

	lines := log.lines()
	require.Len(t, lines, 1)
	require.Equal(t, []string{"quoted-1", "quoted-2"}, lines[0].Quotes)
}

// Several replies to one message stay in time order beneath it.
func TestChatLog_SiblingsStayInTimeOrder(t *testing.T) {
	log := newChatLog(chatActivity)
	log.add(chatEvent("root", "author", "question", 100, ""))
	log.add(chatEvent("late", "other", "later answer", 300, "root"))
	log.add(chatEvent("early", "other", "earlier answer", 200, "root"))

	require.Equal(t, []string{"question", "earlier answer", "later answer"}, contents(log.lines()))
}
