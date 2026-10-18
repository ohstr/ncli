// Package metrics turns relay activity into Prometheus metrics. Metrics
// implements nmilat's relay.Observer and relay.StoreObserver: every hot-path
// method touches only label children resolved at construction, so it never
// allocates or takes a lock beyond the counters' own atomics.
package metrics

import (
	"time"

	"github.com/ohstr/nmilat/relay"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Namespace prefixes every metric this package registers.
const Namespace = "ncli_relay"

var (
	latencyBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
	sendBuckets    = []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, 1}
	countBuckets   = []float64{0, 1, 5, 10, 50, 100, 250, 500, 1000, 5000}
)

// Metrics holds the relay's registry and the pre-resolved series the hot
// path writes to.
type Metrics struct {
	Registry *prometheus.Registry
	started  time.Time

	sessionsTotal   prometheus.Counter
	messages        [relayMessageTypes]prometheus.Counter
	events          [relayEventResults]prometheus.Counter
	rejected        [relayRejectReasons]prometheus.Counter
	byClass         [relayKindClasses]prometheus.Counter
	eventOK         prometheus.Observer
	reqEOSE         prometheus.Observer
	reqEvents       prometheus.Observer
	readRefused     [relayRejectReasons]prometheus.Counter
	countSeconds    prometheus.Observer
	subscriptions   prometheus.Gauge
	subLimitHits    prometheus.Counter
	authAccepted    prometheus.Counter
	authRejected    [relayRejectReasons]prometheus.Counter
	sendSeconds     prometheus.Observer
	sendErrors      prometheus.Counter
	slowSends       prometheus.Counter
	batchSeconds    prometheus.Observer
	batchSize       prometheus.Observer
	batchErrors     prometheus.Counter
	scanBusy        prometheus.Counter
	httpRequests    *prometheus.CounterVec
	httpSeconds     *prometheus.HistogramVec
	adminDenied     *prometheus.CounterVec
	jobRuns         *prometheus.CounterVec
}

const (
	relayMessageTypes  = 7
	relayEventResults  = 4
	relayRejectReasons = 11
	relayKindClasses   = 17
)

// slowSend matches nmilat's own slow-send warning threshold.
const slowSend = 250 * time.Millisecond

// New builds the registry with every hot-path series pre-created at zero, so
// a dashboard sees each one from the first scrape.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	f := factory{reg}
	m := &Metrics{Registry: reg, started: time.Now()}

	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f.gauge("build_info", "Build version of the running relay.", prometheus.Labels{"version": version}).Set(1)
	f.gaugeFunc("uptime_seconds", "Seconds since the relay started.", func() float64 { return time.Since(m.started).Seconds() })

	m.sessionsTotal = f.counter("ws_sessions_total", "WebSocket sessions opened.", nil)

	msgs := f.counterVec("messages_total", "Client messages received, by type.", "type")
	for _, t := range relay.MessageTypes() {
		m.messages[t] = msgs.WithLabelValues(t.String())
	}
	events := f.counterVec("events_total", "EVENTs handled, by outcome.", "result")
	for _, r := range relay.EventResults() {
		m.events[r] = events.WithLabelValues(r.String())
	}
	rejected := f.counterVec("events_rejected_total", "EVENTs rejected, by NIP-01 reason.", "reason")
	refused := f.counterVec("reads_refused_total", "REQs and COUNTs refused, by NIP-01 reason.", "reason")
	authRejected := f.counterVec("nip42_auth_rejected_total", "NIP-42 AUTH attempts rejected, by reason.", "reason")
	for _, r := range relay.RejectReasons() {
		if r == relay.ReasonNone {
			continue
		}
		m.rejected[r] = rejected.WithLabelValues(r.String())
		m.readRefused[r] = refused.WithLabelValues(r.String())
		m.authRejected[r] = authRejected.WithLabelValues(r.String())
	}
	m.authAccepted = f.counter("nip42_auth_accepted_total", "NIP-42 AUTH attempts accepted.", nil)
	byClass := f.counterVec("events_by_class_total", "EVENTs handled, by kind class.", "class")
	for _, k := range relay.KindClasses() {
		m.byClass[k] = byClass.WithLabelValues(k.String())
	}

	m.eventOK = f.histogram("event_ok_seconds", "Time from receiving an EVENT to its OK.", latencyBuckets)
	m.reqEOSE = f.histogram("req_eose_seconds", "Time from a REQ to its EOSE.", latencyBuckets)
	m.reqEvents = f.histogram("req_events_returned", "Stored events delivered per REQ before EOSE.", countBuckets)
	m.countSeconds = f.histogram("count_seconds", "Time to answer a COUNT.", latencyBuckets)
	m.subscriptions = f.gauge("subscriptions_active", "Open subscriptions across all sessions.", nil)
	m.subLimitHits = f.counter("subscriptions_limit_hits_total", "REQs refused for exceeding a session's subscription limit.", nil)
	m.sendSeconds = f.histogram("ws_send_seconds", "Time to write one message to a client.", sendBuckets)
	m.sendErrors = f.counter("ws_send_errors_total", "Failed writes to clients.", nil)
	m.slowSends = f.counter("ws_slow_sends_total", "Writes to clients slower than 250ms.", nil)
	m.batchSeconds = f.histogram("store_batch_seconds", "Time to commit one store write batch.", latencyBuckets)
	m.batchSize = f.histogram("store_batch_size", "Tasks per store write batch.", countBuckets)
	m.batchErrors = f.counter("store_batch_errors_total", "Store write batches that failed.", nil)
	m.scanBusy = f.counter("scan_busy_total", "Reads refused for want of a scan slot.", nil)

	m.httpRequests = f.counterVec("http_requests_total", "HTTP bridge requests, by route and status class.", "route", "code")
	m.httpSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace, Name: "http_request_seconds", Help: "HTTP bridge request duration, by route.", Buckets: latencyBuckets,
	}, []string{"route"})
	reg.MustRegister(m.httpSeconds)
	m.adminDenied = f.counterVec("admin_denied_total", "Admin listener requests refused, by reason.", "reason")
	for _, r := range []string{"cidr", "token", "nip98", "session", "csrf", "replay"} {
		m.adminDenied.WithLabelValues(r)
	}
	m.jobRuns = f.counterVec("job_runs_total", "Index jobs finished, by index and outcome.", "index", "result")
	return m
}

// SessionOpened implements relay.Observer.
func (m *Metrics) SessionOpened() { m.sessionsTotal.Inc() }

// SessionClosed implements relay.Observer.
func (m *Metrics) SessionClosed() {}

// Message implements relay.Observer.
func (m *Metrics) Message(t relay.MessageType) {
	if int(t) < len(m.messages) {
		m.messages[t].Inc()
	}
}

// EventHandled implements relay.Observer.
func (m *Metrics) EventHandled(r relay.EventResult, reason relay.RejectReason, k relay.KindClass, d time.Duration) {
	if int(r) < len(m.events) {
		m.events[r].Inc()
	}
	if r == relay.EventRejected && reason != relay.ReasonNone && int(reason) < len(m.rejected) {
		m.rejected[reason].Inc()
	}
	if int(k) < len(m.byClass) {
		m.byClass[k].Inc()
	}
	m.eventOK.Observe(d.Seconds())
}

// ReqServed implements relay.Observer.
func (m *Metrics) ReqServed(d time.Duration, events int) {
	m.reqEOSE.Observe(d.Seconds())
	m.reqEvents.Observe(float64(events))
}

// ReadRefused implements relay.Observer.
func (m *Metrics) ReadRefused(reason relay.RejectReason) {
	if reason != relay.ReasonNone && int(reason) < len(m.readRefused) {
		m.readRefused[reason].Inc()
	}
}

// CountServed implements relay.Observer.
func (m *Metrics) CountServed(d time.Duration) { m.countSeconds.Observe(d.Seconds()) }

// SubscriptionsChanged implements relay.Observer.
func (m *Metrics) SubscriptionsChanged(delta int) { m.subscriptions.Add(float64(delta)) }

// SubscriptionLimitHit implements relay.Observer.
func (m *Metrics) SubscriptionLimitHit() { m.subLimitHits.Inc() }

// Auth implements relay.Observer.
func (m *Metrics) Auth(accepted bool, reason relay.RejectReason) {
	if accepted {
		m.authAccepted.Inc()
		return
	}
	if reason == relay.ReasonNone {
		reason = relay.ReasonOther
	}
	if int(reason) < len(m.authRejected) {
		m.authRejected[reason].Inc()
	}
}

// Sent implements relay.Observer.
func (m *Metrics) Sent(d time.Duration, failed bool) {
	m.sendSeconds.Observe(d.Seconds())
	if failed {
		m.sendErrors.Inc()
	} else if d > slowSend {
		m.slowSends.Inc()
	}
}

// BatchCommitted implements relay.StoreObserver.
func (m *Metrics) BatchCommitted(n int, d time.Duration, failed bool) {
	m.batchSeconds.Observe(d.Seconds())
	m.batchSize.Observe(float64(n))
	if failed {
		m.batchErrors.Inc()
	}
}

// ScanBusy implements relay.StoreObserver.
func (m *Metrics) ScanBusy() { m.scanBusy.Inc() }

// AdminDenied counts one admin listener refusal. reason is one of a fixed
// set: cidr, token, nip98, session, csrf, replay.
func (m *Metrics) AdminDenied(reason string) { m.adminDenied.WithLabelValues(reason).Inc() }

// JobFinished counts one finished index job.
func (m *Metrics) JobFinished(index, result string) { m.jobRuns.WithLabelValues(index, result).Inc() }

var (
	_ relay.Observer      = (*Metrics)(nil)
	_ relay.StoreObserver = (*Metrics)(nil)
)

// factory registers metrics under Namespace.
type factory struct{ reg *prometheus.Registry }

func (f factory) counter(name, help string, labels prometheus.Labels) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help, ConstLabels: labels})
	f.reg.MustRegister(c)
	return c
}

func (f factory) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) gauge(name, help string, labels prometheus.Labels) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: name, Help: help, ConstLabels: labels})
	f.reg.MustRegister(g)
	return g
}

func (f factory) gaugeFunc(name, help string, fn func() float64) {
	f.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: Namespace, Name: name, Help: help}, fn))
}

func (f factory) counterFunc(name, help string, fn func() float64) {
	f.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: Namespace, Name: name, Help: help}, fn))
}

func (f factory) histogram(name, help string, buckets []float64) prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: Namespace, Name: name, Help: help, Buckets: buckets})
	f.reg.MustRegister(h)
	return h
}
