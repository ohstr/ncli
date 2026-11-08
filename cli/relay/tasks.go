package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	"gopkg.in/yaml.v3"
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
//   - * for every task except group:<kind>: those must be listed (group:*
//     or a kind), so no role moderates other people's groups by accident.
//
// A pubkey's tasks are the union of what its NIP-43 roles carry, plus the
// members grant when it is enrolled. A banned pubkey holds none.
//
// nip11.pubkey and nip86.admins hold * and keep it through a ban, so the
// relay can't lock its own operator out of NIP-86. They hold no group:<kind>:
// moderating the groups the relay hosts belongs to groups.admins (and the
// relay key), which nmilat answers itself (SetRelayAdmins), never through
// this authority. Role ids are canonical lowercase (canonicalRole).

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
	if s.tasks[task] {
		return true
	}
	if isGroupKindTask(task) {
		return s.groupAll
	}
	return s.all
}

// isGroupKindTask reports whether task is group:<kind> or group:*, which *
// does not cover.
func isGroupKindTask(task string) bool {
	return strings.HasPrefix(task, taskGroupPrefix)
}

// delegatesGroupTasks reports whether s holds any group:<kind>.
func (s *taskSet) delegatesGroupTasks() bool {
	if s == nil {
		return false
	}
	if s.groupAll {
		return true
	}
	for t := range s.tasks {
		if isGroupKindTask(t) {
			return true
		}
	}
	return false
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
		if kind == nip29.KindCreateGroup {
			// Creating a group acts on no existing group, so nmilat never
			// asks the authorizer about it.
			return fmt.Errorf("%q would never apply: creating a group is the group.create task (see groups.create)", task)
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
	if m != nil && m.Roles != nil {
		// Role ids are canonical lowercase. Viper already lowercases config
		// keys (checkRoleKeysInFile catches what that merges); this covers a
		// config built in code.
		roles := make(map[string]RoleTasks, len(m.Roles))
		for _, id := range sortedKeys(m.Roles) {
			canon := canonicalRole(id)
			switch {
			case canon == "":
				return taskConfigError("membership.roles", errors.New("membership.roles has an empty role id"))
			case isReservedRoleID(canon):
				return taskConfigError(id, fmt.Errorf("membership.roles.%s: %q is reserved (a groups.create keyword); name the role something else", id, canon))
			}
			if _, dup := roles[canon]; dup {
				return taskConfigError(id, fmt.Errorf("membership.roles names role %q twice, in different case: role ids are case-insensitive", canon))
			}
			roles[canon] = m.Roles[id]
		}
		m.Roles = roles
	}
	if m != nil {
		for _, id := range sortedKeys(m.Roles) {
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
		for i, entry := range c.Groups.Create {
			// Keywords and role ids alike are case-insensitive.
			entry = canonicalRole(entry)
			c.Groups.Create[i] = entry
			switch {
			case entry == "":
				return taskConfigError("groups.create", errors.New("groups.create has an empty entry"))
			case entry == groupCreateEveryone:
			case !enabled:
				return taskConfigError(entry, fmt.Errorf("groups.create %q requires membership.enabled: only everyone works without NIP-43 membership", entry))
			}
		}
	}
	return nil
}

// isReservedRoleID reports whether id is a groups.create keyword, which a
// NIP-43 role may not be named: groups.create could not tell them apart.
func isReservedRoleID(id string) bool {
	id = canonicalRole(id)
	return id == groupCreateEveryone || id == groupCreateMembers
}

// checkRoleKeysInFile reads membership.roles from the config file as
// written, since viper has already lowercased (and so merged) its keys and
// split any key holding a dot: a role id written twice in different case
// (Host and host), or one with a dot, is refused rather than half-applied.
func checkRoleKeysInFile(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // reading the config already reported it
	}
	var doc struct {
		Membership struct {
			Roles yaml.Node `yaml:"roles"`
		} `yaml:"membership"`
	}
	if yaml.Unmarshal(data, &doc) != nil || doc.Membership.Roles.Kind != yaml.MappingNode {
		return nil
	}
	seen := map[string]string{}
	content := doc.Membership.Roles.Content
	for i := 0; i+1 < len(content); i += 2 {
		id := content[i].Value
		if strings.Contains(id, ".") {
			return taskConfigError(id, fmt.Errorf("membership.roles: role id %q contains a dot, which a config key cannot hold: name the role without one", id))
		}
		canon := canonicalRole(id)
		if prev, dup := seen[canon]; dup {
			return taskConfigError(id, fmt.Errorf("membership.roles names role %q twice (%q and %q): role ids are case-insensitive", canon, prev, id))
		}
		seen[canon] = id
	}
	return nil
}

// delegatesGroupTasks reports whether membership.roles or
// membership.members gives anyone a group:<kind> task.
func (c *RelayConfig) delegatesGroupTasks() bool {
	m := c.Membership
	if m == nil || !m.Enabled {
		return false
	}
	var sets []RoleTasks
	for _, rt := range m.Roles {
		sets = append(sets, rt)
	}
	if m.Members != nil {
		sets = append(sets, *m.Members)
	}
	for _, rt := range sets {
		var s taskSet
		for _, task := range rt.Can {
			s.add(task)
		}
		if s.delegatesGroupTasks() {
			return true
		}
	}
	return false
}

// grantCeiling is groups.grantCeiling as applied: an explicit value wins;
// unset, it is on exactly when relay roles carry group:<kind> tasks, so a
// key that holds a group kind only through its relay role cannot make
// itself a group admin that outlives the role.
func (c *RelayConfig) grantCeiling() bool {
	if c.Groups != nil && c.Groups.GrantCeiling != nil {
		return *c.Groups.GrantCeiling
	}
	return c.delegatesGroupTasks()
}

// warnRoleTasks logs what a valid config still probably doesn't mean: with
// membership.members.can set, a member gets only what it lists, so a feature
// restricted to members (blossom.membersOnly, huddle.requireMembership) that
// it leaves out is now restricted to the admins and the roles that list it.
// It also warns when groups.grantCeiling is turned off although roles carry
// group:<kind> tasks.
func (c *RelayConfig) warnRoleTasks() {
	if c.Groups != nil && c.Groups.GrantCeiling != nil && !*c.Groups.GrantCeiling && c.delegatesGroupTasks() {
		log.Warn().Str("setting", "groups.grantCeiling").
			Msg("groups.grantCeiling is false while membership roles carry group:<kind> tasks: a role holding group:9000 can make its holder a group admin who keeps that seat after the role is revoked")
	}
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
		if c.blossomUploadMembersOnly() {
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

// groupCreateRestricted reports whether kind 9007 needs group.create:
// groups.create is set and does not say everyone.
func (c *RelayConfig) groupCreateRestricted() bool {
	return c.Groups != nil && c.Groups.Create != nil && !slices.ContainsFunc(c.Groups.Create, func(e string) bool { return canonicalRole(e) == groupCreateEveryone })
}

// taskGrants is the config resolved into who holds what, independent of the
// live membership state.
type taskGrants struct {
	// admins hold * (every task but group:<kind>): nip11.pubkey and
	// nip86.admins. groups.admins are nmilat's relay admins, not here.
	admins map[string]bool
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
//   - nip11.pubkey, nip86.admins: *
//   - memberClaims: members get createclaim
//   - blossom.membersOnly.upload/download: members get blossom.upload/download
//     (off: everyone does)
//   - huddle.requireMembership: members get huddle.join (off: everyone does)
//   - groups.create unset or with everyone: everyone gets group.create
//
// membership.members.can, when set, is the members grant on its own: the old
// switches then only decide whether a task is restricted.
func resolveTaskGrants(c *RelayConfig) taskGrants {
	g := taskGrants{admins: map[string]bool{}, roles: map[string]*taskSet{}}
	if c.Nip11.PubKey != "" {
		g.admins[strings.ToLower(c.Nip11.PubKey)] = true
	}
	if c.Nip86 != nil {
		for _, a := range c.Nip86.Admins {
			g.admins[strings.ToLower(a)] = true
		}
	}

	if c.Blossom == nil || !c.blossomUploadMembersOnly() {
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
			if c.blossomUploadMembersOnly() {
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
			if canonicalRole(entry) == groupCreateMembers {
				g.members.add(taskGroupCreate)
			} else {
				g.role(entry).add(taskGroupCreate)
			}
		}
	}
	return g
}

func (g *taskGrants) role(id string) *taskSet {
	id = canonicalRole(id)
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

// holder is one pubkey's standing, read once: the membership record is
// fetched at most once however many tasks are asked about (supportedmethods
// asks about every method).
type holder struct {
	a             *taskAuthority
	pk            string
	admin, banned bool
	loaded        bool
	sets          []*taskSet // the members grant and its roles' sets, once loaded
}

func (a *taskAuthority) holderFor(pubkey string) *holder {
	pk := strings.ToLower(pubkey)
	return &holder{a: a, pk: pk, admin: a.grants.admins[pk], banned: a.moderation.bannedPubkey(pk)}
}

// load reads pk's membership: nothing when not enrolled, else the members
// grant plus each NIP-43 role's set.
func (h *holder) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	m := h.a.membership
	if m == nil || !m.IsMember(h.pk) {
		return
	}
	h.sets = append(h.sets, &h.a.grants.members)
	if len(h.a.grants.roles) == 0 {
		return
	}
	rec, err := m.Get(h.pk)
	if err != nil || rec == nil {
		return
	}
	for _, id := range rec.Roles {
		if set := h.a.grants.roles[canonicalRole(id)]; set != nil {
			h.sets = append(h.sets, set)
		}
	}
}

// has reports whether the pubkey may do task.
func (h *holder) has(task string) bool {
	if h.admin && !isGroupKindTask(task) {
		return true
	}
	if h.banned {
		return false
	}
	if h.a.grants.everyone.has(task) {
		return true
	}
	h.load()
	for _, s := range h.sets {
		if s.has(task) {
			return true
		}
	}
	return false
}

// anyNip86 reports whether the pubkey holds at least one NIP-86 method.
func (h *holder) anyNip86() bool {
	if h.admin {
		return true
	}
	if h.banned {
		return false
	}
	h.load()
	for _, s := range h.sets {
		if s.anyNip86() {
			return true
		}
	}
	return false
}

// can reports whether pubkey may do task.
func (a *taskAuthority) can(pubkey, task string) bool {
	return a.holderFor(pubkey).has(task)
}

// mayUseNip86 reports whether pubkey holds any NIP-86 method, which is what
// admits it to the management API (Router.Visible then confines it).
func (a *taskAuthority) mayUseNip86(pubkey string) bool {
	return a.holderFor(pubkey).anyNip86()
}

// supportedMethods answers NIP-86 supportedmethods for caller: of every
// registered method (all), those it holds, read with one membership lookup.
func (a *taskAuthority) supportedMethods(all []string) nip86.Method {
	return func(_ context.Context, caller string, _ nip86.Request) (any, error) {
		h := a.holderFor(caller)
		out := []string{}
		for _, m := range all {
			if m == nip86.MethodSupportedMethods || h.has(m) {
				out = append(out, m)
			}
		}
		return out, nil
	}
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

// groupAuthorizer is the GroupsService authorizer for group:<kind> tasks: it
// lets a pubkey send NIP-29 moderation kind in any hosted group when one of
// its NIP-43 roles (or the members grant) lists group:<kind> or group:*. *
// does not count, and nor does being in nip86.admins: group-wide power is
// groups.admins' (SetRelayAdmins). It can only add power: nmilat ORs it with
// the group's own roles.
func (a *taskAuthority) groupAuthorizer(_, pubkey string, kind int) bool {
	return nip29.IsModerationKind(kind) && a.can(pubkey, taskGroupPrefix+strconv.Itoa(kind))
}

// applyGroupTasks installs groupAuthorizer and groups.grantCeiling on the
// groups service.
func applyGroupTasks(groups *relay.GroupsService, a *taskAuthority, c *RelayConfig) {
	groups.SetAuthorizer(a.groupAuthorizer)
	groups.SetGrantCeiling(c.grantCeiling())
}

// inviteRequestAllowed decides a NIP-43 invite request (REQ kind 28935): the
// same task as NIP-86 createclaim, held by any of the session's identities.
// A NIP-AA virtual identity counts only for what its own key holds, as with
// createclaim over NIP-86: it is not an enrolled member. It is the session's
// InviteRequestAuthorizer; nmilat sends the refusal as `restricted: ...`.
func (a *taskAuthority) inviteRequestAllowed(ids []relay.AuthedIdentity) error {
	for _, id := range ids {
		if a.can(id.Pubkey, nip86.MethodCreateClaim) {
			return nil
		}
	}
	return errors.New("you may not invite to this relay")
}

// warnUndefinedRoles logs each membership.roles id with no kind 33534
// definition in store, and each defined role named like a groups.create
// keyword. Neither is an error: roles are created at runtime.
func warnUndefinedRoles(c *RelayConfig, defined []string) {
	known := map[string]bool{}
	for _, id := range defined {
		known[canonicalRole(id)] = true
		if isReservedRoleID(id) {
			log.Warn().Str("role", id).Msg("a kind 33534 role is named like a groups.create keyword (everyone, members): groups.create reads the keyword, never this role")
		}
	}
	if c.Membership == nil {
		return
	}
	for _, id := range sortedKeys(c.Membership.Roles) {
		if !known[canonicalRole(id)] {
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
