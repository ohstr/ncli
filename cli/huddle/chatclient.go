package huddle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/rs/zerolog/log"
)

// chatHistoryLimit is how much backlog the chat subscription asks for, so
// someone joining a call mid-conversation sees what was already said instead
// of an empty panel.
const chatHistoryLimit = 200

// chatSendTimeout bounds how long a send waits for the relay's OK before
// reporting it unconfirmed.
const chatSendTimeout = 10 * time.Second

// chatSender is the part of the chat client the panel consumes, as an
// interface so the panel can be driven without a relay in tests.
type chatSender interface {
	send(ctx context.Context, content, parent string, quotes []string) error
	self() string
}

// chatClient owns the Nostr relay connection chat needs. The huddle audio
// socket cannot carry it: kind:1311 messages are Nostr events, and that socket
// speaks only Opus frames and a handful of JSON control messages.
//
// # Why SubscribeWithID + Read rather than Subscribe
//
// Connection.Subscribe closes its channel at EOSE, which is exactly wrong for
// chat: EOSE marks the end of the *stored* history, and everything said from
// then on is what a conversation actually is. SubscribeWithID leaves the
// subscription open, and Read gives every message on the connection.
//
// That makes this type the connection's single reader, which is also why
// sending goes through Send plus OK-matching here rather than
// Connection.Publish -- Publish drains the same shared channel, so running it
// beside this loop would have the two stealing each other's messages.
type chatClient struct {
	conn     *relayclient.Connection
	activity string
	// relayHint goes in the "a" tag so a reader knows where to find the
	// activity the message belongs to.
	relayHint string
	privKey   string
	pubkey    string
	subID     string

	incoming chan *nip01.Event

	mu      sync.Mutex
	pending map[string]chan error

	closeOnce sync.Once
}

// dialChat connects to relayURL and opens the chat subscription for activity.
func dialChat(ctx context.Context, relayURL *url.URL, activity, relayHint, privKeyHex, pubkey string) (*chatClient, error) {
	conn, err := relayclient.Connect(ctx, relayURL)
	if err != nil {
		return nil, err
	}

	c := &chatClient{
		conn:      conn,
		activity:  activity,
		relayHint: relayHint,
		privKey:   privKeyHex,
		pubkey:    pubkey,
		subID:     uuid.NewString(),
		incoming:  make(chan *nip01.Event, chatHistoryLimit),
		pending:   map[string]chan error{},
	}

	// "a" is a single-letter tag, so the relay does this filtering rather than
	// shipping every chat message on the relay to be discarded here.
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip53.KindLiveChatMessage},
		Tags:  map[string][]string{"a": {activity}},
		Limit: chatHistoryLimit,
	})
	if !conn.SubscribeWithID(c.subID, filters) {
		conn.Close()
		return nil, errors.New("connection closed before the chat subscription opened")
	}

	go c.run(ctx)
	return c, nil
}

// messages is the stream of chat events for this activity. It is closed when
// the connection goes away.
func (c *chatClient) messages() <-chan *nip01.Event { return c.incoming }

// self is the pubkey this client signs as.
func (c *chatClient) self() string { return c.pubkey }

// run is the connection's only reader: it routes events to the panel and
// resolves each send's OK back to whoever is waiting for it.
func (c *chatClient) run(ctx context.Context) {
	defer close(c.incoming)

	for {
		select {
		case res, ok := <-c.conn.Read():
			if !ok {
				c.failPending(errors.New("relay connection closed"))
				return
			}
			switch m := res.(type) {
			case *wire.EventSubscriptionResponse:
				if m.SubscriptionID != c.subID || m.Event == nil {
					continue
				}
				select {
				case c.incoming <- m.Event:
				case <-ctx.Done():
					return
				}
			case *wire.OkSubscriptionResponse:
				c.resolve(m)
			case *wire.NoticeSubscriptionResponse:
				log.Warn().Str("notice", m.Message).Msg("relay notice on the chat connection")
			}

		case err := <-c.conn.Errors():
			c.failPending(err)
			return
		case <-c.conn.Closed():
			c.failPending(errors.New("relay connection closed"))
			return
		case <-ctx.Done():
			c.failPending(ctx.Err())
			return
		}
	}
}

// send publishes a kind:1311 message and waits for the relay to accept it.
// parent, when set, is the event id this replies to; quotes are event ids
// referenced without replying.
func (c *chatClient) send(ctx context.Context, content, parent string, quotes []string) error {
	event := nip53.NewLiveChatMessage(nip53.LiveChatMessageParams{
		Pubkey:        c.pubkey,
		Activity:      c.activity,
		ActivityRelay: c.relayHint,
		Parent:        parent,
		Quotes:        quotes,
		Content:       content,
	})
	// Sign fills in the id and signature, so the ack below has something to
	// match on.
	if err := event.Sign(c.privKey); err != nil {
		return fmt.Errorf("failed to sign the message: %w", err)
	}

	ack := make(chan error, 1)
	c.mu.Lock()
	c.pending[event.ID] = ack
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, event.ID)
		c.mu.Unlock()
	}()

	if !c.conn.Send(event) {
		return errors.New("relay connection closed during send")
	}

	select {
	case err := <-ack:
		return err
	case <-time.After(chatSendTimeout):
		// The message may still land; what is certain is that the relay did
		// not say so in time, and claiming it was sent would be a guess.
		return errors.New("the relay did not confirm the message in time")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resolve matches an OK to the send waiting for it.
func (c *chatClient) resolve(ok *wire.OkSubscriptionResponse) {
	c.mu.Lock()
	ack, waiting := c.pending[ok.EventID]
	c.mu.Unlock()
	if !waiting {
		return
	}
	var err error
	if !ok.Accepted {
		err = fmt.Errorf("relay rejected the message: %s", ok.Message)
	}
	select {
	case ack <- err:
	default:
	}
}

// failPending unblocks every in-flight send when the connection goes away, so
// a send cannot sit waiting for an OK that can no longer arrive.
func (c *chatClient) failPending(err error) {
	c.mu.Lock()
	acks := make([]chan error, 0, len(c.pending))
	for _, ack := range c.pending {
		acks = append(acks, ack)
	}
	c.mu.Unlock()

	for _, ack := range acks {
		select {
		case ack <- err:
		default:
		}
	}
}

func (c *chatClient) close() {
	c.closeOnce.Do(func() {
		c.conn.CloseSubscription(c.subID)
		c.conn.Close()
	})
}
