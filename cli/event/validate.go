package event

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip22"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/nip34"
	"github.com/ohstr/nmilat/nip43"
	"github.com/ohstr/nmilat/nip53"
	"github.com/ohstr/nmilat/nip57"
	"github.com/ohstr/nmilat/nip65"
	"github.com/ohstr/nmilat/nip71"
	"github.com/ohstr/nmilat/nip88"
	"github.com/ohstr/nmilat/nip90"
	"github.com/ohstr/nmilat/nipA0"
	"github.com/ohstr/nmilat/nipAZ"
	"github.com/ohstr/nmilat/nipB0"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/spf13/cobra"
)

// validator is one nmilat structural check for a kind.
type validator struct {
	Name string
	Fn   func(*nip01.Event) error
}

// validators maps a kind to the check nmilat runs on it. Ranges are
// handled in validatorFor.
var validators = map[int]validator{
	nip22.KindComment:                {"nip22.ValidateComment", nip22.ValidateComment},
	nip29.KindGroupMetadata:          {"nip29.ValidateGroupMetadata", nip29.ValidateGroupMetadata},
	nip29.KindGroupAdmins:            {"nip29.ValidateGroupAdmins", nip29.ValidateGroupAdmins},
	nip29.KindGroupMembers:           {"nip29.ValidateGroupMembers", nip29.ValidateGroupMembers},
	nip29.KindGroupRoles:             {"nip29.ValidateGroupRoles", nip29.ValidateGroupRoles},
	nip29.KindLiveParticipants:       {"nip29.ValidateLiveParticipants", nip29.ValidateLiveParticipants},
	nip29.KindGroupPinnedEvents:      {"nip29.ValidateGroupPinnedEvents", nip29.ValidateGroupPinnedEvents},
	nip29.KindJoinRequest:            {"nip29.ValidateJoinRequest", nip29.ValidateJoinRequest},
	nip29.KindLeaveRequest:           {"nip29.ValidateLeaveRequest", nip29.ValidateLeaveRequest},
	nip34.KindRepositoryAnnouncement: {"nip34.ValidateRepositoryAnnouncement", nip34.ValidateRepositoryAnnouncement},
	nip34.KindRepositoryState:        {"nip34.ValidateRepositoryState", nip34.ValidateRepositoryState},
	nip34.KindPatch:                  {"nip34.ValidatePatch", nip34.ValidatePatch},
	nip34.KindPullRequest:            {"nip34.ValidatePullRequest", nip34.ValidatePullRequest},
	nip34.KindPullRequestUpdate:      {"nip34.ValidatePullRequestUpdate", nip34.ValidatePullRequestUpdate},
	nip34.KindIssue:                  {"nip34.ValidateIssue", nip34.ValidateIssue},
	nip34.KindStatusOpen:             {"nip34.ValidateStatus", nip34.ValidateStatus},
	nip34.KindStatusApplied:          {"nip34.ValidateStatus", nip34.ValidateStatus},
	nip34.KindStatusClosed:           {"nip34.ValidateStatus", nip34.ValidateStatus},
	nip34.KindStatusDraft:            {"nip34.ValidateStatus", nip34.ValidateStatus},
	nip34.KindGraspServerList:        {"nip34.ValidateGraspServerList", nip34.ValidateGraspServerList},
	nip43.KindRoleDefinition:         {"nip43.ValidateRole", nip43.ValidateRole},
	nip43.KindMembershipList:         {"nip43.ValidateMembershipList", nip43.ValidateMembershipList},
	nip43.KindAddUser:                {"nip43.ValidateAddUser", nip43.ValidateAddUser},
	nip43.KindRemoveUser:             {"nip43.ValidateRemoveUser", nip43.ValidateRemoveUser},
	nip43.KindInviteRequest:          {"nip43.ValidateInviteResponse", nip43.ValidateInviteResponse},
	nip53.KindLiveChatMessage:        {"nip53.ValidateLiveChatMessage", nip53.ValidateLiveChatMessage},
	nip53.KindRoomPresence:           {"nip53.ValidateRoomPresence", nip53.ValidateRoomPresence},
	nip53.KindLiveStreamingEvent:     {"nip53.ValidateLiveStream", nip53.ValidateLiveStream},
	nip53.KindMeetingSpace:           {"nip53.ValidateMeetingSpace", nip53.ValidateMeetingSpace},
	nip53.KindMeetingRoomEvent:       {"nip53.ValidateMeetingRoomEvent", nip53.ValidateMeetingRoomEvent},
	nip57.KindZapRequest:             {"nip57.ValidateZapRequestForRelay", nip57.ValidateZapRequestForRelay},
	nip57.KindZapReceipt:             {"nip57.ValidateZapReceiptForRelay", nip57.ValidateZapReceiptForRelay},
	nip65.KindRelayListMetadata:      {"nip65.ValidateRelayList", nip65.ValidateRelayList},
	nip71.KindVideo:                  {"nip71.ValidateVideo", nip71.ValidateVideo},
	nip71.KindShortVideo:             {"nip71.ValidateVideo", nip71.ValidateVideo},
	nip71.KindAddressableVideo:       {"nip71.ValidateVideo", nip71.ValidateVideo},
	nip71.KindAddressableShortVideo:  {"nip71.ValidateVideo", nip71.ValidateVideo},
	nip88.KindPoll:                   {"nip88.ValidatePoll", nip88.ValidatePoll},
	nipA0.KindVoiceMessage:           {"nipA0.ValidateVoiceMessage", nipA0.ValidateVoiceMessage},
	nipA0.KindVoiceMessageReply:      {"nipA0.ValidateVoiceMessage", nipA0.ValidateVoiceMessage},
	nipAZ.KindAltZapReceipt:          {"nipAZ.ValidateAltZapReceiptForRelay", nipAZ.ValidateAltZapReceiptForRelay},
	nipB0.KindWebBookmark:            {"nipB0.ValidateWebBookmark", nipB0.ValidateWebBookmark},
	nipB7.KindBlossomServerList:      {"nipB7.ValidateBlossomServerList", nipB7.ValidateBlossomServerList},
	nipB7.KindAuthorization:          {"nipB7.ValidateAuthorization", nipB7.ValidateAuthorization},
	nipB7.KindReport:                 {"nipB7.ValidateReport", nipB7.ValidateReport},
	nipIC.KindIdentityConnection:     {"nipIC.ValidateIdentityConnection", nipIC.ValidateIdentityConnection},
	nipIC.KindAttestation:            {"nipIC.ValidateAttestation", nipIC.ValidateAttestation},
	nip90.KindJobFeedback:            {"nip90.ValidateJobFeedback", nip90.ValidateJobFeedback},
}

func validatorFor(kind int) (validator, bool) {
	if v, ok := validators[kind]; ok {
		return v, true
	}
	switch {
	case nip29.IsModerationKind(kind):
		return validator{"nip29.ValidateModerationAction", nip29.ValidateModerationAction}, true
	case kind >= 5000 && kind < 6000:
		return validator{"nip90.ValidateJobRequest", nip90.ValidateJobRequest}, true
	case kind >= 6000 && kind < 7000:
		return validator{"nip90.ValidateJobResult", nip90.ValidateJobResult}, true
	}
	return validator{}, false
}

type validateReport struct {
	Input     string `json:"input"`
	ID        string `json:"id"`
	Kind      int    `json:"kind"`
	Status    string `json:"status"` // valid, invalid
	Signature check  `json:"signature"`
	Structure check  `json:"structure"`
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [ref...]",
		Short: "Check events against nmilat's per-kind rules",
		Long: `Runs the structural check an nmilat relay applies to each event's kind
(NIP-29 groups, NIP-22 comments, NIP-57 zaps, NIP-65 relay lists, NIP-34
git, NIP-53 live events, ...), plus the signature. A kind nmilat has no
rule for reports structure "skip".

Use it to see why a relay would reject an event before publishing it.
Exits 1 if any event is invalid.`,
		Example: `  ncli event validate -f group-metadata.json --offline
  ncli event validate naddr1...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			refs, single, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()

			lookups := res.resolve(ctx, refs, false)
			hits, missing := found(lookups)
			reports := make([]validateReport, 0, len(hits))
			invalid := 0
			for _, l := range hits {
				rep := validateEvent(l.best().Event)
				rep.Input = l.Ref.Input
				if rep.Status != "valid" {
					invalid++
				}
				reports = append(reports, rep)
			}

			if jsonMode(cmd) {
				if len(reports) > 0 || len(missing) == 0 {
					if err := printShaped(cmd, reports, single); err != nil {
						return err
					}
				}
			} else {
				for _, r := range reports {
					outf(cmd, "%-7s  %s  kind %d\n", r.Status, r.ID, r.Kind)
					for _, c := range []check{r.Signature, r.Structure} {
						line := fmt.Sprintf("  %-4s %s", c.Status, c.Name)
						if c.Detail != "" {
							line += ": " + c.Detail
						}
						outf(cmd, "%s\n", line)
					}
				}
			}
			if invalid > 0 {
				return common.RuntimeError(cmd, fmt.Errorf("%d of %d event(s) are invalid", invalid, len(reports)))
			}
			return missingError(cmd, res, missing)
		},
	}
}

func validateEvent(ev *nip01.Event) validateReport {
	rep := validateReport{ID: ev.ID, Kind: ev.Kind}
	if err := ev.Verify(); err != nil {
		rep.Signature = fail("signature", err.Error())
	} else {
		rep.Signature = ok("signature", "")
	}
	if v, has := validatorFor(ev.Kind); has {
		if err := v.Fn(ev); err != nil {
			rep.Structure = fail("structure", v.Name+": "+err.Error())
		} else {
			rep.Structure = ok("structure", v.Name)
		}
	} else {
		rep.Structure = skip("structure", fmt.Sprintf("no nmilat rule for kind %d", ev.Kind))
	}
	rep.Status = verdict([]check{rep.Signature, rep.Structure})
	return rep
}
