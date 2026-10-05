package relay

// eventsEnabled reports whether to mount the POST /events bridge. Mirrors
// queryEnabled's shape -- see EventsConfig's doc comment.
func eventsEnabled() bool {
	return config.HTTPBridge != nil && config.HTTPBridge.Events != nil && config.HTTPBridge.Events.Enabled
}
