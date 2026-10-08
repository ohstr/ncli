package relay

import (
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/rs/zerolog/log"
)

// startPprof serves net/http/pprof on its own listener, so a stuck relay can
// be profiled (e.g. /debug/pprof/goroutine?debug=2) without killing it.
func startPprof(addr string) {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Warn().Str("addr", addr).Msg("pprof is listening on a non-loopback address; it is unauthenticated")
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		log.Info().Str("addr", addr).Msg("pprof listening")
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Error().Err(err).Str("addr", addr).Msg("pprof listener stopped")
		}
	}()
}
