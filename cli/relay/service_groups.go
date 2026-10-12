package relay

import (
	"fmt"
	"sort"

	"github.com/ohstr/nmilat/nip29"
)

// GroupsConfig configures NIP-29 relay-hosted groups. Omitting the
// `groups:` block keeps nmilat's default: only the "admin" role may
// moderate, and it may do everything.
//
//	groups:
//	  roles:
//	    admin: [9000, 9001, 9002, 9005, 9008, 9009, 9010]
//	    moderator: [9005, 9010]
type GroupsConfig struct {
	// Roles maps each group role name to the moderation kinds (9000-9020) a
	// member holding it may send. A group's creator holds "admin", so it
	// must be listed. Role names are lowercase: config keys are.
	Roles map[string][]int `mapstructure:"roles"`
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
