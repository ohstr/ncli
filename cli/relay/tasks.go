package relay

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog/log"
)

// Roles and tasks. NIP-29, NIP-43 and NIP-86 leave what a role may do to the
// relay; membership.roles is where this relay says it. A task is named after
// the protocol verb that performs it:
//
//   - a NIP-86 method name (createclaim, banpubkey, ...), or an alias for a
//     family of them (@claims, @members, @bans, @roles, @relayinfo, @policy);
//   - group:<kind> for one NIP-29 moderation kind in every hosted group, or
//     group:* for all of them;
//   - an ncli name where no protocol verb exists: blossom.upload,
//     blossom.download, blossom.delete (any blob), huddle.join, group.create;
//   - * for everything.
//
// A pubkey's tasks are the union of what its NIP-43 roles carry, plus the
// members grant when it is enrolled. A banned pubkey holds none. The keys the
// config file names (nip11.pubkey and nip86.admins: everything; groups.admins:
// group:*) keep them regardless of a ban: only an edit of the file takes them
// away, so a relay can never lock its own operator out.

// Task names that are not NIP-86 methods.
const (
	taskAll             = "*"
	taskGroupAll        = "group:*"
	taskGroupPrefix     = "group:"
	taskBlossomUpload   = "blossom.upload"
	taskBlossomDownload = "blossom.download"
	taskBlossomDelete   = "blossom.delete"
	taskHuddleJoin      = "huddle.join"
	taskGroupCreate     = "group.create"
)

// groups.create entries other than a role id.
const (
	groupCreateEveryone = "everyone"
	groupCreateMembers  = "members"
)

// nip86Tasks are the NIP-86 methods this relay serves, by alias. Every method
// is in exactly one family, so the families together are the full list.
var nip86TaskAliases = map[string][]string{
	"@claims":  {nip86.MethodCreateClaim, nip86MethodCreateClaimWithLimits, nip86MethodGetClaim, nip86.MethodListClaims, nip86.MethodDeleteClaim},
	"@members": {nip86.MethodAllowPubkey, nip86.MethodUnallowPubkey, nip86.MethodListAllowedPubkeys},
	"@bans": {nip86.MethodBanPubkey, nip86.MethodUnbanPubkey, nip86.MethodListBannedPubkeys,
		methodBanEvent, methodAllowEvent, methodListBannedEvents, methodListEventsNeedingModeration},
	"@roles":     {nip86.MethodCreateRole, nip86.MethodEditRole, nip86.MethodDeleteRole, nip86.MethodAssignRole, nip86.MethodUnassignRole},
	"@relayinfo": {methodChangeRelayName, methodChangeRelayDescription, methodChangeRelayIcon},
	"@policy": {methodAllowKind, methodDisallowKind, methodListAllowedKinds, methodListDisallowedKinds,
		methodBlockIP, methodUnblockIP, methodListBlockedIPs},
}

// nip86Methods is every NIP-86 method a role may be given, flattened.
var nip86Methods = func() map[string]bool {
	out := map[string]bool{}
	for _, methods := range nip86TaskAliases {
		for _, m := range methods {
			out[m] = true
		}
	}
	return out
}()

// ncliTasks are the tasks no protocol verb names.
var ncliTasks = map[string]bool{
	taskBlossomUpload: true, taskBlossomDownload: true, taskBlossomDelete: true,
	taskHuddleJoin: true, taskGroupCreate: true,
}

// RoleTasks is what one role (or every member) may do.
//
//	membership:
//	  roles:
//	    host:    { can: ["@claims", "@members", banpubkey, "group:9005"] }
//	    greeter: { can: [createclaim] }
//	  members:   { can: [createclaim] }
type RoleTasks struct {
	Can []string `mapstructure:"can"`
}

// taskSet is a resolved list of tasks: aliases expanded, * and group:* kept
// as flags.
type taskSet struct {
	all, groupAll bool
	tasks         map[string]bool
}

func (s *taskSet) add(task string) {
	if s.tasks == nil {
		s.tasks = map[string]bool{}
	}
	switch {
	case task == taskAll:
		s.all = true
	case task == taskGroupAll:
		s.groupAll = true
	case nip86TaskAliases[task] != nil:
		for _, m := range nip86TaskAliases[task] {
			s.tasks[m] = true
		}
	default:
		s.tasks[task] = true
	}
}

func (s *taskSet) has(task string) bool {
	if s == nil {
		return false
	}
	if s.all || s.tasks[task] {
		return true
	}
	return s.groupAll && strings.HasPrefix(task, taskGroupPrefix)
}

// anyNip86 reports whether s holds at least one NIP-86 method.
func (s *taskSet) anyNip86() bool {
	if s == nil {
		return false
	}
	if s.all {
		return true
	}
	for t := range s.tasks {
		if nip86Methods[t] {
			return true
		}
	}
	return false
}

// checkTask reports whether task is a name membership.roles accepts.
func checkTask(task string) error {
	switch {
	case task == taskAll, task == taskGroupAll, nip86Methods[task], nip86TaskAliases[task] != nil, ncliTasks[task]:
		return nil
	case task == nip86.MethodSupportedMethods:
		// Always answered, to anyone the API admits; harmless to name.
		return nil
	case strings.HasPrefix(task, taskGroupPrefix):
		kind, err := strconv.Atoi(strings.TrimPrefix(task, taskGroupPrefix))
		if err != nil || !nip29.IsModerationKind(kind) {
			return fmt.Errorf("%q is not a NIP-29 moderation kind: want group:<9000-9020> or group:*", task)
		}
		return nil
	}
	return fmt.Errorf("unknown task %q: want a NIP-86 method, an alias (@claims, @members, @bans, @roles, @relayinfo, @policy), group:<kind>, group:*, blossom.upload, blossom.download, blossom.delete, huddle.join, group.create or *", task)
}

func taskConfigError(input string, err error) error {
	return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: input}
}

// checkRoleTasks validates membership.roles, membership.members and
// groups.create. It runs before memberClaims is defaulted, so it can tell an
// explicit memberClaims from an unset one.
func (c *RelayConfig) checkRoleTasks() error {
	m := c.Membership
	enabled := m != nil && m.Enabled
	if m != nil && !enabled {
		if len(m.Roles) > 0 {
			return taskConfigError("membership.roles", errors.New("membership.roles requires membership.enabled"))
		}
		if m.Members != nil {
			return taskConfigError("membership.members", errors.New("membership.members requires membership.enabled"))
		}
	}
	if m != nil {
		for _, id := range sortedKeys(m.Roles) {
			if strings.TrimSpace(id) == "" {
				return taskConfigError("membership.roles", errors.New("membership.roles has an empty role id"))
			}
			for _, task := range m.Roles[id].Can {
				if err := checkTask(task); err != nil {
					return taskConfigError(task, fmt.Errorf("membership.roles.%s: %w", id, err))
				}
			}
		}
		if m.Members != nil {
			for _, task := range m.Members.Can {
				if err := checkTask(task); err != nil {
					return taskConfigError(task, fmt.Errorf("membership.members: %w", err))
				}
			}
			// memberClaims and members.can both say whether a member may
			// createclaim; an explicit memberClaims must agree.
			if mc := m.MemberClaims; mc != nil {
				var members taskSet
				for _, task := range m.Members.Can {
					members.add(task)
				}
				if *mc != members.has(nip86.MethodCreateClaim) {
					return taskConfigError("membership.memberClaims", fmt.Errorf(
						"membership.memberClaims is %t but membership.members.can %s createclaim: leave memberClaims unset, or make them agree",
						*mc, map[bool]string{true: "lists", false: "does not list"}[members.has(nip86.MethodCreateClaim)]))
				}
			}
		}
	}
	if c.Groups != nil {
		for _, entry := range c.Groups.Create {
			switch {
			case strings.TrimSpace(entry) == "":
				return taskConfigError("groups.create", errors.New("groups.create has an empty entry"))
			case entry == groupCreateEveryone:
			case !enabled:
				return taskConfigError(entry, fmt.Errorf("groups.create %q requires membership.enabled: only everyone works without NIP-43 membership", entry))
			}
		}
	}
	return nil
}

// warnRoleTasks logs what a valid config still probably doesn't mean: with
// membership.members.can set, a member gets only what it lists, so a feature
// restricted to members (blossom.membersOnly, huddle.requireMembership) that
// it leaves out is now restricted to the admins and the roles that list it.
func (c *RelayConfig) warnRoleTasks() {
	m := c.Membership
	if m == nil || !m.Enabled || m.Members == nil {
		return
	}
	var members taskSet
	for _, task := range m.Members.Can {
		members.add(task)
	}
	restricted := map[string]string{}
	if c.Blossom != nil && c.Blossom.Enabled {
		if c.blossomUploadRestricted() {
			restricted[taskBlossomUpload] = "blossom.membersOnly.upload"
		}
		if c.Blossom.MembersOnly.Download {
			restricted[taskBlossomDownload] = "blossom.membersOnly.download"
		}
	}
	if huddleEnabled(c.Huddle) && c.Huddle.RequireMembership {
		restricted[taskHuddleJoin] = "huddle.requireMembership"
	}
	for _, task := range sortedKeys(restricted) {
		if !members.has(task) {
			log.Warn().Str("task", task).Str("setting", restricted[task]).
				Msg("membership.members.can does not list this task: only admins and roles that list it may do it")
		}
	}
}

// blossomUploadRestricted reports whether Blossom uploads are limited to
// holders of blossom.upload. Unset, it follows membership_required.
func (c *RelayConfig) blossomUploadRestricted() bool {
	up := c.Blossom.MembersOnly.Upload
	return (up == nil && c.Nip11.Limitation.MembershipRequired) || (up != nil && *up)
}

// groupCreateRestricted reports whether kind 9007 needs group.create:
// groups.create is set and does not say everyone.
func (c *RelayConfig) groupCreateRestricted() bool {
	return c.Groups != nil && c.Groups.Create != nil && !slices.Contains(c.Groups.Create, groupCreateEveryone)
}

// taskGrants is the config resolved into who holds what, independent of the
// live membership state.
type taskGrants struct {
	// admins hold every task: nip11.pubkey and nip86.admins.
	admins map[string]bool
	// groupAdmins hold group:*: groups.admins.
	groupAdmins map[string]bool
	// everyone holds these unless banned: what the old switches leave open.
	everyone taskSet
	// members is what every enrolled member holds.
	members taskSet
	// roles maps a lowercase NIP-43 role id to what it carries.
	roles map[string]*taskSet
}

// resolveTaskGrants maps the config, old switches included, onto grants.
// With only the old switches set it reproduces their behaviour exactly:
//
//   - nip11.pubkey, nip86.admins: *; groups.admins: group:*
//   - memberClaims: members get createclaim
//   - blossom.membersOnly.upload/download: members get blossom.upload/download
//     (off: everyone does)
//   - huddle.requireMembership: members get huddle.join (off: everyone does)
//   - groups.create unset or with everyone: everyone gets group.create
//
// membership.members.can, when set, is the members grant on its own: the old
// switches then only decide whether a task is restricted.
func resolveTaskGrants(c *RelayConfig) taskGrants {
	g := taskGrants{admins: map[string]bool{}, groupAdmins: map[string]bool{}, roles: map[string]*taskSet{}}
	if c.Nip11.PubKey != "" {
		g.admins[strings.ToLower(c.Nip11.PubKey)] = true
	}
	if c.Nip86 != nil {
		for _, a := range c.Nip86.Admins {
			g.admins[strings.ToLower(a)] = true
		}
	}
	if admins, err := groupAdmins(c); err == nil {
		for _, a := range admins {
			g.groupAdmins[strings.ToLower(a)] = true
		}
	}

	if c.Blossom == nil || !c.blossomUploadRestricted() {
		g.everyone.add(taskBlossomUpload)
	}
	if c.Blossom == nil || !c.Blossom.MembersOnly.Download {
		g.everyone.add(taskBlossomDownload)
	}
	if c.Huddle == nil || !c.Huddle.RequireMembership {
		g.everyone.add(taskHuddleJoin)
	}
	if !c.groupCreateRestricted() {
		g.everyone.add(taskGroupCreate)
	}

	m := c.Membership
	if m == nil || !m.Enabled {
		return g
	}
	if m.Members != nil {
		for _, task := range m.Members.Can {
			g.members.add(task)
		}
	} else {
		if c.memberClaims() {
			g.members.add(nip86.MethodCreateClaim)
		}
		if c.Blossom != nil {
			if c.blossomUploadRestricted() {
				g.members.add(taskBlossomUpload)
			}
			if c.Blossom.MembersOnly.Download {
				g.members.add(taskBlossomDownload)
			}
		}
		if c.Huddle != nil && c.Huddle.RequireMembership {
			g.members.add(taskHuddleJoin)
		}
	}
	for id, rt := range m.Roles {
		set := g.role(id)
		for _, task := range rt.Can {
			set.add(task)
		}
	}
	if c.groupCreateRestricted() {
		for _, entry := range c.Groups.Create {
			if entry == groupCreateMembers {
				g.members.add(taskGroupCreate)
			} else {
				g.role(entry).add(taskGroupCreate)
			}
		}
	}
	return g
}

func (g *taskGrants) role(id string) *taskSet {
	id = strings.ToLower(id)
	set := g.roles[id]
	if set == nil {
		set = &taskSet{}
		g.roles[id] = set
	}
	return set
}

// taskAuthority answers can(pubkey, task) against the grants and the live
// membership and ban state.
type taskAuthority struct {
	grants     taskGrants
	membership *relay.MembershipService
	moderation *relayModeration
}

func newTaskAuthority(c *RelayConfig, membership *relay.MembershipService, moderation *relayModeration) *taskAuthority {
	return &taskAuthority{grants: resolveTaskGrants(c), membership: membership, moderation: moderation}
}

// can reports whether pubkey may do task.
func (a *taskAuthority) can(pubkey, task string) bool {
	pk := strings.ToLower(pubkey)
	if a.grants.admins[pk] {
		return true
	}
	if a.grants.groupAdmins[pk] && strings.HasPrefix(task, taskGroupPrefix) {
		return true
	}
	if a.moderation.bannedPubkey(pk) {
		return false
	}
	if a.grants.everyone.has(task) {
		return true
	}
	return a.memberCan(pk, func(s *taskSet) bool { return s.has(task) })
}

// mayUseNip86 reports whether pubkey holds any NIP-86 method, which is what
// admits it to the management API (Router.Visible then confines it).
func (a *taskAuthority) mayUseNip86(pubkey string) bool {
	pk := strings.ToLower(pubkey)
	if a.grants.admins[pk] {
		return true
	}
	if a.moderation.bannedPubkey(pk) {
		return false
	}
	return a.memberCan(pk, (*taskSet).anyNip86)
}

// memberCan reports whether pk is enrolled and the members grant, or one of
// its NIP-43 roles, satisfies ok.
func (a *taskAuthority) memberCan(pk string, ok func(*taskSet) bool) bool {
	if a.membership == nil || !a.membership.IsMember(pk) {
		return false
	}
	if ok(&a.grants.members) {
		return true
	}
	if len(a.grants.roles) == 0 {
		return false
	}
	rec, err := a.membership.Get(pk)
	if err != nil || rec == nil {
		return false
	}
	for _, id := range rec.Roles {
		if set := a.grants.roles[strings.ToLower(id)]; set != nil && ok(set) {
			return true
		}
	}
	return false
}

// nip86Visible is Router.Visible: supportedmethods to anyone admitted, and
// each other method to whoever holds it, so supportedmethods answers each
// caller with its own set.
func (a *taskAuthority) nip86Visible(caller, method string) bool {
	return method == nip86.MethodSupportedMethods || a.can(caller, method)
}

// gateGroupCreate wraps next so that, when groups.create restricts it, kind
// 9007 (create group) from a pubkey without group.create is refused. It runs
// as the session's EventPolicy, before the groups service sees the event.
func (a *taskAuthority) gateGroupCreate(restricted bool, next relay.EventPolicy) relay.EventPolicy {
	return func(ev *nip01.Event) string {
		if next != nil {
			if reason := next(ev); reason != "" {
				return reason
			}
		}
		if restricted && ev != nil && ev.Kind == nip29.KindCreateGroup && !a.can(ev.PubKey, taskGroupCreate) {
			return "restricted: you may not create groups on this relay"
		}
		return ""
	}
}

// inviteRequestAllowed decides a NIP-43 invite request (REQ kind 28935): the
// same task as NIP-86 createclaim, held by any of the session's identities.
// A NIP-AA virtual identity counts only for what its own key holds, as with
// createclaim over NIP-86: it is not an enrolled member.
//
// TODO(nmilat#236): wire this as SessionConfig.InviteRequestAuthorizer
// (relay.WithInviteRequestAuthorizer) once go.mod pins an nmilat with it:
//
//	sessionConfig.InviteRequestAuthorizer = func(_ context.Context, s *relay.Session) error {
//		return auth.inviteRequestAllowed(s.Identities())
//	}
//
// Until then any member may still request an invite over REQ 28935.
func (a *taskAuthority) inviteRequestAllowed(ids []relay.AuthedIdentity) error {
	for _, id := range ids {
		if a.can(id.Pubkey, nip86.MethodCreateClaim) {
			return nil
		}
	}
	return errors.New("you may not invite to this relay")
}

// warnUndefinedRoles logs each membership.roles id with no kind 33534
// definition in store. Not an error: a role can be created at runtime.
func warnUndefinedRoles(c *RelayConfig, defined []string) {
	if c.Membership == nil || len(c.Membership.Roles) == 0 {
		return
	}
	known := map[string]bool{}
	for _, id := range defined {
		known[strings.ToLower(id)] = true
	}
	for _, id := range sortedKeys(c.Membership.Roles) {
		if !known[strings.ToLower(id)] {
			log.Warn().Str("role", id).Msg("membership.roles names a role with no kind 33534 definition yet; its tasks apply once a member holds it")
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
