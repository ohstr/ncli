package huddle

import (
	"context"
	"errors"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/nip01"
)

// joinEvent is one line of `space join --json`'s NDJSON stream.
type joinEvent struct {
	Type      string    `json:"type"`
	Time      time.Time `json:"time"`
	Room      string    `json:"room,omitempty"`
	Activity  string    `json:"activity,omitempty"`
	Self      string    `json:"self,omitempty"`
	Pubkey    string    `json:"pubkey,omitempty"`
	ID        string    `json:"id,omitempty"`
	Content   string    `json:"content,omitempty"`
	CreatedAt uint64    `json:"created_at,omitempty"`
	Parent    string    `json:"parent,omitempty"`
	Quotes    []string  `json:"quotes,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// Reasons an `ended` event carries.
const (
	endDuration     = "duration"
	endInterrupted  = "interrupted"
	endDisconnected = "disconnected"
)

// runHeadless is Board.Run without a screen: it emits a joinEvent per
// change -- arrivals, departures, speaking, chat -- until ctx ends or the
// call drops. durationCtx, if done first, ends it with reason "duration".
func runHeadless(ctx, durationCtx context.Context, hc Client, room, activity string, chat chatSource, emit func(joinEvent)) error {
	self := hc.Self().Pubkey
	emit(joinEvent{Type: "joined", Time: time.Now(), Room: room, Activity: activity, Self: self})

	r := newRoster(self, SpeakingHold, huddleclient.DefaultSpeakingThreshold, time.Now)
	present := map[string]bool{}
	speaking := map[string]bool{}

	var chatMessages <-chan *nip01.Event
	var log *chatLog
	if chat != nil {
		chatMessages = chat.messages()
		log = newChatLog(activity)
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	frames := hc.Frames()
	for {
		select {
		case <-durationCtx.Done():
			reason := endInterrupted
			if ctx.Err() == nil {
				reason = endDuration
			}
			emit(joinEvent{Type: "ended", Time: time.Now(), Reason: reason})
			return nil

		case event, ok := <-chatMessages:
			if !ok {
				chatMessages = nil
				continue
			}
			if !log.add(event) {
				continue
			}
			m := log.get(event.ID)
			emit(joinEvent{Type: "chat", Time: time.Now(), ID: m.ID, Pubkey: m.Author,
				Content: m.Content, CreatedAt: m.CreatedAt, Parent: m.Parent, Quotes: m.Quotes})

		case frame, ok := <-frames:
			if !ok {
				emit(joinEvent{Type: "ended", Time: time.Now(), Reason: endDisconnected})
				if err := hc.Err(); err != nil {
					return err
				}
				return errors.New("the call ended")
			}
			r.heardFrame(frame)

		case <-ticker.C:
			r.sync(hc.Roster())
			now := map[string]bool{}
			for _, p := range r.participants() {
				if p.Self {
					continue
				}
				now[p.Pubkey] = true
				if !present[p.Pubkey] {
					emit(joinEvent{Type: "participant_joined", Time: time.Now(), Pubkey: p.Pubkey})
				}
				if p.Speaking != speaking[p.Pubkey] {
					typ := "speaking_stopped"
					if p.Speaking {
						typ = "speaking_started"
					}
					emit(joinEvent{Type: typ, Time: time.Now(), Pubkey: p.Pubkey})
					speaking[p.Pubkey] = p.Speaking
				}
			}
			for pubkey := range present {
				if !now[pubkey] {
					if speaking[pubkey] {
						emit(joinEvent{Type: "speaking_stopped", Time: time.Now(), Pubkey: pubkey})
						delete(speaking, pubkey)
					}
					emit(joinEvent{Type: "participant_left", Time: time.Now(), Pubkey: pubkey})
				}
			}
			present = now
		}
	}
}
