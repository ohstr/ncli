package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip98"
)

// liveRoom is one occupied room in a GET /huddle/rooms response.
type liveRoom struct {
	ID    string `json:"id"`
	Peers int    `json:"peers"`
	// ProtocolVersion is what the room is pinned to by its first peer. A
	// client speaking anything else is refused at join, so it is worth
	// knowing before dialing rather than after.
	ProtocolVersion uint8 `json:"protocol_version"`
}

// huddleRoomsHandler answers GET /huddle/rooms with the calls that are live
// right now. Rooms are created on join and dropped when the last peer leaves,
// so this is the whole truth: there is no durable set of rooms to list, and a
// room nobody is in does not exist.
//
// Authorization mirrors joining rather than the /admin routes: the list is
// exactly the set of rooms the caller could already walk into, so it leaks
// nothing a join would not, and gating it behind an admin key would make it
// useless to the joiner who wants it. With no membership requirement there is
// nothing to check and the list is open; with one, the caller authenticates
// over NIP-98 and goes through the same authorize hook a join does.
func huddleRoomsHandler(rooms *room.Manager, authorize func(ctx context.Context, roomID, pubkey string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if authorize != nil {
			pubkey, err := nip98Pubkey(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			// Body is the empty slice, not nil, so the payload tag every ncli
			// client sends is actually checked against the (empty) body.
			if _, err := nip98.Verify(r, nip98.Options{
				AllowedPubkeys: []string{pubkey},
				Body:           []byte{},
			}); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			// The hook takes a room id, and there is no one room being asked
			// about here. Membership is per relay, not per room, so it ignores
			// the id -- but a future per-room rule would need its own call.
			if err := authorize(r.Context(), "", pubkey); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{"rooms": listLiveRooms(rooms)})
	}
}

// listLiveRooms snapshots the occupied rooms, sorted by id so repeated calls
// and a scripted diff see a stable order.
//
// Ending a room removes it from the manager under the same lock, so an ended
// room should never appear here. It is still filtered out, along with one that
// emptied between the two snapshots below: "live" is the one thing this
// endpoint promises, and a room already closing is not somewhere a caller can
// join.
func listLiveRooms(rooms *room.Manager) []liveRoom {
	occupancy := rooms.Occupancy()
	live := make([]liveRoom, 0, len(occupancy))
	for id, peers := range occupancy {
		if peers == 0 {
			continue
		}
		r, ok := rooms.Get(id)
		if !ok || r.Ended() {
			continue
		}
		live = append(live, liveRoom{ID: id, Peers: peers, ProtocolVersion: r.ProtocolVersion()})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })
	return live
}

// nip98Pubkey reads the pubkey a NIP-98 Authorization header claims, without
// trusting it. nip98.Verify fails closed on an empty AllowedPubkeys, so an
// endpoint open to any identity has to name the one it is checking; the
// signature, URL, method and freshness are all still verified against it, and
// the trust decision stays with the authorize hook.
func nip98Pubkey(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Nostr ") {
		return "", nip98.ErrMissingAuthHeader
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Nostr "))
	if err != nil {
		return "", nip98.ErrMissingAuthHeader
	}
	var event nip01.Event
	if err := json.Unmarshal(raw, &event); err != nil || event.PubKey == "" {
		return "", nip98.ErrInvalidNIP98Event
	}
	return event.PubKey, nil
}
