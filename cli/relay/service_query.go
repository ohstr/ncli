package relay

// queryEnabled reports whether to mount the POST /query bridge. Independent
// of membership/nip86 -- see QueryConfig's doc comment on why NIP-98 there
// is identity-binding, not an authorization gate.
func queryEnabled() bool {
	return config.Query != nil && config.Query.Enabled
}
