package relay

import (
	"fmt"
	"sort"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/relay"
)

// GroupsConfig configures NIP-29 relay-hosted groups. Omitting the
// `groups:` block keeps nmilat's defaults: only the "admin" role moderates,
// only the relay's own key moderates every group, and moderation events and
// codeless joins to closed groups are not kept.
//
//	groups:
//	  roles:
//	    admin: [9000, 9001, 9002, 9005, 9008, 9009, 9010]
//	    moderator: [9005, 9010]
//	  roleDescriptions:
//	    moderator: keeps the peace
//	  admins: [<npub or hex>]
//	  moderationLog: true
//	  pendingJoins: true
type GroupsConfig struct {
	// Roles maps each group role name to the moderation kinds (9000-9020) a
	// member holding it may send. A group's creator holds "admin", so it
	// must be listed. Role names are lowercase: config keys are.
	Roles map[string][]int `mapstructure:"roles"`

	// RoleDescriptions is the text each role gets in the group's kind
	// 39003. A role without one is described by what it may do.
	RoleDescriptions map[string]string `mapstructure:"roleDescriptions"`

	// Admins are keys (npub or hex) that moderate every hosted group, like
	// the relay's own key always does: e.g. to recover a group whose admins
	// are gone.
	Admins []string `mapstructure:"admins"`

	// ModerationLog keeps accepted moderation events, readable by the
	// group's members (invites by its admins), so clients can show who did
	// what.
	ModerationLog bool `mapstructure:"moderationLog"`

	// PendingJoins keeps a closed group's join request that has no invite
	// code, for its admins to accept (put-user) or decline (remove-user),
	// instead of refusing it.
	PendingJoins bool `mapstructure:"pendingJoins"`

	// MembershipNotices signs a seat notice for each member gained or lost;
	// see MembershipNoticesConfig.
	MembershipNotices *MembershipNoticesConfig `mapstructure:"membershipNotices"`

	// Create says who may create a group (kind 9007): everyone, members, or
	// a NIP-43 role id, any number of them. Omitted, everyone may. An empty
	// list leaves it to the relay's admins and the roles whose
	// membership.roles tasks list group.create.
	Create []string `mapstructure:"create"`

	// GrantCeiling stops a member from granting a role that may send a
	// moderation kind it may not send itself (put-user with roles, kind
	// 9000): no handing out more power than you hold. Unset, it is on
	// exactly when membership roles (or members) carry a group:<kind> task,
	// and off otherwise, since it refuses grants existing groups may rely on.
	// An explicit value wins; false with group tasks delegated is a warning.
	GrantCeiling *bool `mapstructure:"grantCeiling"`
}

// groupModerationPolicy is the moderation policy groups.roles describes, or
// nil when it isn't set (nmilat's default applies).
func groupModerationPolicy(cfg *RelayConfig) (nip29.ModerationPolicy, error) {
	if cfg.Groups == nil || len(cfg.Groups.Roles) == 0 {
		return nil, nil
	}
	if _, ok := cfg.Groups.Roles["admin"]; !ok {
		return nil, fmt.Errorf("groups.roles must define \"admin\": a group's creator holds it")
	}
	names := make([]string, 0, len(cfg.Groups.Roles))
	for name := range cfg.Groups.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	policy := nip29.ModerationPolicy{}
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("groups.roles has an empty role name")
		}
		kinds := cfg.Groups.Roles[name]
		for _, k := range kinds {
			if !nip29.IsModerationKind(k) {
				return nil, fmt.Errorf("groups.roles.%s: %d is not a NIP-29 moderation kind (9000-9020)", name, k)
			}
		}
		policy[name] = append([]int(nil), kinds...)
	}
	return policy, nil
}

// groupAdmins resolves groups.admins to hex pubkeys.
func groupAdmins(cfg *RelayConfig) ([]string, error) {
	if cfg.Groups == nil {
		return nil, nil
	}
	out := make([]string, 0, len(cfg.Groups.Admins))
	for _, raw := range cfg.Groups.Admins {
		pk, err := nip19.ParsePublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("groups.admins: %q: %w", raw, err)
		}
		out = append(out, pk)
	}
	return out, nil
}

// checkGroupsConfig validates the groups block at startup.
func checkGroupsConfig(cfg *RelayConfig) error {
	policy, err := groupModerationPolicy(cfg)
	if err != nil {
		return err
	}
	if _, err := groupAdmins(cfg); err != nil {
		return err
	}
	if cfg.Groups == nil {
		return nil
	}
	for role := range cfg.Groups.RoleDescriptions {
		known := role == "admin"
		if policy != nil {
			_, known = policy[role]
		}
		if !known {
			return fmt.Errorf("groups.roleDescriptions.%s: no such role in groups.roles", role)
		}
	}
	return nil
}

// groupsSessionOptions are the groups block's settings the session handler
// must have before it is built: the moderation policy (groups.roles) and
// the role descriptions. NewSessionHandler republishes every hosted
// group's kind:39001 and kind:39003 at startup, so setting them afterwards
// would first publish both under the default policy, then again.
func groupsSessionOptions(cfg *RelayConfig) []relay.SessionOption {
	if cfg.Groups == nil {
		return nil
	}
	var opts []relay.SessionOption
	if policy, _ := groupModerationPolicy(cfg); policy != nil {
		opts = append(opts, relay.WithGroupsModerationPolicy(policy))
	}
	if len(cfg.Groups.RoleDescriptions) > 0 {
		opts = append(opts, relay.WithGroupsRoleDescriptions(cfg.Groups.RoleDescriptions))
	}
	return opts
}

// applyGroupsConfig configures the rest of the relay's groups service from
// the groups block (already validated by checkGroupsConfig); the policy and
// role descriptions come in at construction (groupsSessionOptions). The
// relay's own key stays a group admin alongside groups.admins.
func applyGroupsConfig(groups *relay.GroupsService, cfg *RelayConfig) {
	if cfg.Groups == nil {
		return
	}
	if admins, _ := groupAdmins(cfg); len(admins) > 0 {
		groups.SetRelayAdmins(append([]string{cfg.Nip11.Self}, admins...)...)
	}
	groups.SetModerationLog(cfg.Groups.ModerationLog)
	groups.SetPendingJoins(cfg.Groups.PendingJoins)
	if kinds, ok := cfg.memberNotices(); ok {
		groups.SetMemberNotices(kinds)
	}
}
