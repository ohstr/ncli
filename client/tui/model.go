package tui

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	MaxLogEntries = 10000 // Maximum log entries to keep in memory
)

type LogLevel int

const (
	LogLevelTrace LogLevel = iota
	LogLevelDebug
	LogLevelInfo
	LogLevelWarn
	LogLevelError
	LogLevelSuccess
)

type FlowStat interface {
	Columns() []*Column
	Row() []string
	FlatRow() []int

	IncreaseFailures()
	IncreaseLost()
	Lost() int
	AddEvent(int, string)
	IncSynced()
	GetAttributes() FlowAttr
	IncreaseRetries(time.Time)
	EOSECount() int
	ResetEOSECounter()
	SetIndexWidth(int)

	Close()
}

/////////

type FlowLog interface{}

type FlowLogText struct {
	text      string
	createdAt time.Time
	FlowAttr
}

type FlowLogCounter struct {
	count       int
	lastEventID string
	FlowLogText
}

func NewFlowLogCounter(attr FlowAttr, eid string) *FlowLogCounter {
	return &FlowLogCounter{
		FlowLogText: FlowLogText{
			FlowAttr:  attr,
			createdAt: time.Now(),
		},
		lastEventID: eid,
	}
}

type FlowAttr struct {
	Index     int
	Name      string
	FlagColor tcell.Color
}

type FlowLogger struct {
	Level      LogLevel
	logs       []FlowLog
	lock       sync.Mutex
	lastIndex  int
	Raw        bool
	indexWidth int
}

// SetIndexWidth sets the digit width used to pad rendered flag indices (see
// renderFlag). Owned per-logger rather than as shared package state so that
// concurrent Streams/Inspectors each render against their own flow count
// without racing on a global.
func (fl *FlowLogger) SetIndexWidth(n int) {
	fl.lock.Lock()
	defer fl.lock.Unlock()
	fl.indexWidth = n
}

func (fl *FlowLogger) shouldLog(level LogLevel) bool {
	return level >= fl.Level
}

// Enabled reports whether a message at level would actually be logged,
// letting hot-path callers skip building an expensive message (e.g. a
// per-event fmt.Sprintf in a loop) when it would just be discarded.
func (fl *FlowLogger) Enabled(level LogLevel) bool {
	return fl.shouldLog(level)
}

func (fl *FlowLogger) LogEvent(eid string, attr FlowAttr) {
	fl.lock.Lock()
	defer fl.lock.Unlock()

	var logIns *FlowLogCounter

	for i := len(fl.logs) - 1; i >= fl.lastIndex; i-- {
		if lastLog, ok := fl.logs[i].(*FlowLogCounter); ok && attr.Index == lastLog.Index && attr.FlagColor == lastLog.FlagColor {
			logIns = lastLog
			break
		}
	}

	if logIns == nil {
		logIns = NewFlowLogCounter(attr, eid)
		fl.logs = append(fl.logs, logIns)
	}

	logIns.count++

	if fl.Raw {
		fmt.Printf("%s EVENT count=%d %s\n", formatTimestamp(time.Now()), logIns.count, eid)
	}

	// Trim logs if exceeding max size
	fl.trimLogs()
}

func (fl *FlowLogger) Log(text string, attr FlowAttr) {
	fl.lock.Lock()
	defer fl.lock.Unlock()
	fl.logs = append(fl.logs, &FlowLogText{
		FlowAttr:  attr,
		text:      text,
		createdAt: time.Now(),
	})

	if fl.Raw {
		fmt.Printf("%s INF [%s] %s\n", formatTimestamp(time.Now()), attr.Name, text)
	}

	// Trim logs if exceeding max size
	fl.trimLogs()
}

func (fl *FlowLogger) Error(err error, attr FlowAttr) {
	if fl.shouldLog(LogLevelError) {
		msg := fmt.Sprintf("[red]●[-] %s", err.Error())
		fl.Log(msg, attr)
		if fl.Raw {
			fmt.Fprintf(os.Stderr, "%s ERR [%s] %v\n", formatTimestamp(time.Now()), attr.Name, err)
		}
	}
}

func (fl *FlowLogger) Warn(text string, attr FlowAttr) {
	if fl.shouldLog(LogLevelWarn) {
		fl.Log(fmt.Sprintf("[yellow]●[-] %s", text), attr)
	}
}

func (fl *FlowLogger) Success(text string, attr FlowAttr) {
	if fl.shouldLog(LogLevelSuccess) {
		fl.Log(fmt.Sprintf("[green]●[-] %s", text), attr)
	}
}

func (fl *FlowLogger) Info(text string, attr FlowAttr) {
	if fl.shouldLog(LogLevelInfo) {
		fl.Log(fmt.Sprintf("[white]●[-] %s", text), attr)
	}
}

func (fl *FlowLogger) Debug(text string, attr FlowAttr) {
	if fl.shouldLog(LogLevelDebug) {
		fl.Log(fmt.Sprintf("[blue]●[-] %s", text), attr)
	}
}

func (fl *FlowLogger) Trace(text string, attr FlowAttr) {
	if fl.shouldLog(LogLevelTrace) {
		fl.Log(fmt.Sprintf("[gray]●[-] %s", text), attr)
	}
}

// trimLogs removes old log entries to prevent unbounded memory growth
func (fl *FlowLogger) trimLogs() {
	if len(fl.logs) > MaxLogEntries {
		// Keep most recent MaxLogEntries
		removed := len(fl.logs) - MaxLogEntries
		fl.logs = fl.logs[removed:]
		fl.lastIndex -= removed
		if fl.lastIndex < 0 {
			fl.lastIndex = 0
		}
	}
}

func (fl *FlowLogger) GetLastLogs() [][]string {
	fl.lock.Lock()
	defer fl.lock.Unlock()

	logs := fl.logs[fl.lastIndex:]
	fl.lastIndex = len(fl.logs)

	logsArr := [][]string{}

	for _, log := range logs {
		switch l := log.(type) {
		case *FlowLogCounter:
			subArr := []string{fmt.Sprintf("[gray:-:-]%s", formatTimestamp(l.createdAt))}
			if l.Index > 0 {
				subArr = append(subArr, fmt.Sprintf("%s [green]●[-]", renderFlag(l.FlagColor, l.Index, fl.indexWidth)))
			}

			if l.count > 1 {
				subArr = append(subArr, fmt.Sprintf("EVENTS [purple]%d", l.count))
			} else {
				subArr = append(subArr, fmt.Sprintf("EVENT %s", l.lastEventID))
			}
			logsArr = append(logsArr, subArr)

		case *FlowLogText:
			entryLog := []string{fmt.Sprintf("[gray:-:-]%s", formatTimestamp(l.createdAt))}
			if l.Index > 0 {
				entryLog = append(entryLog, renderFlag(l.FlagColor, l.Index, fl.indexWidth))
			}
			entryLog = append(entryLog, l.text)

			logsArr = append(logsArr, entryLog)
		}
	}
	return logsArr
}

/////////

type FlowMetrics struct {
	id        int
	name      string      `header:"Name" sortDefault:"" sortDir:"asc" sortKey:"N"`
	events    int         `header:"Events" sortKey:"E"`
	pubkeys   int         `header:"Pubkeys" sortKey:"P"` // Changed to simple counter to prevent unbounded growth
	kinds     map[int]int `header:"Kinds" sortKey:"K"`
	failures  int         `header:"Failures" sortKey:"F"`
	synced    int         `header:"Synced" sortKey:"S"`
	retries   int         `header:"Retries" sortKey:"R"`
	lost      int         `header:"-"`
	createdAt time.Time   `header:"Age" sortKey:"A"`

	color       tcell.Color
	nextRetry   time.Time
	eoseCounter int
	skipColumns []string

	// trackDiversity gates the pubkeysSeen/kinds bookkeeping in AddEvent, not
	// just their columns' visibility: destinations mirror the same merged
	// event stream from every source, so their diversity numbers would be
	// near-duplicates of each other row-to-row and of Sources -- not worth
	// the per-event map writes. See NewOutboundMetrics.
	trackDiversity bool

	mu          sync.RWMutex
	pubkeysSeen map[string]struct{} // Track unique pubkeys, cleared periodically
	closeFunc   func()
	indexWidth  int
}

func NewFlowMetrics(id int, name string, color tcell.Color, skipColumns []string, trackDiversity bool, closeCallback func()) *FlowMetrics {

	fm := &FlowMetrics{
		id:             id,
		name:           name,
		createdAt:      time.Now(),
		skipColumns:    skipColumns,
		trackDiversity: trackDiversity,

		color: color,

		closeFunc:  closeCallback,
		indexWidth: 1,
	}

	if trackDiversity {
		fm.pubkeysSeen = make(map[string]struct{})
		fm.kinds = make(map[int]int)
	}

	return fm
}

// SetIndexWidth sets the digit width used to pad this row's rendered flag
// index (see renderFlag). Owned per-instance rather than as shared package
// state so that concurrent Streams/Inspectors each render against their own
// flow count without racing on a global.
func (fm *FlowMetrics) SetIndexWidth(n int) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.indexWidth = n
}

func (fm *FlowMetrics) Fields() []*Column {
	fields := []*Column{}
	var index int
	t := reflect.TypeOf(fm).Elem()
	for i := 0; i < t.NumField(); i++ {

		name := t.Field(i).Tag.Get("header")
		if len(name) == 0 || name == "-" || slices.Contains(fm.skipColumns, name) {
			continue
		}

		c := &Column{
			Index: index,
			Name:  name,
		}

		if _, ok := t.Field(i).Tag.Lookup("sortDefault"); ok {
			c.IsSorted = true
		}

		if sortDir := t.Field(i).Tag.Get("sortDir"); sortDir == SortAsc || sortDir == SortDesc {
			c.SortDir = sortDir
		} else {
			c.SortDir = SortDesc // default
		}

		if sortKey, ok := t.Field(i).Tag.Lookup("sortKey"); ok && len(sortKey) == 1 {
			c.SortKey = rune(sortKey[0])
		}

		fields = append(fields, c)
		index++
	}
	return fields
}

func (fm *FlowMetrics) Columns() []*Column {
	return fm.Fields()
}

func (fm *FlowMetrics) Row() []string {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	output := []string{
		fmt.Sprintf(`%s [%s]%s`, renderFlag(fm.color, fm.id, fm.indexWidth), tcell.ColorWhite, fm.name),
		strconv.Itoa(fm.events),
	}

	if !slices.Contains(fm.skipColumns, "Pubkeys") {
		output = append(output, strconv.Itoa(fm.pubkeys))
	}

	if !slices.Contains(fm.skipColumns, "Kinds") {
		output = append(output, strconv.Itoa(len(fm.kinds)))
	}

	// lost is the permanent, unrecoverable subset of failures (see
	// IncreaseLost) -- fold it into this cell instead of a separate column so
	// a healthy row doesn't carry a redundant "0".
	if fm.lost > 0 {
		output = append(output, fmt.Sprintf("%d [red](%d lost)[-]", fm.failures, fm.lost))
	} else {
		output = append(output, strconv.Itoa(fm.failures))
	}

	if !slices.Contains(fm.skipColumns, "Synced") {
		output = append(output, strconv.Itoa(fm.synced))
	}

	nextRetry := time.Until(fm.nextRetry)
	if nextRetry > 0 {
		output = append(output, fmt.Sprintf("%d [red]%s", fm.retries, nextRetry.Truncate(time.Second).String()))
	} else {
		output = append(output, strconv.Itoa(fm.retries))
	}

	output = append(output, time.Since(fm.createdAt).Truncate(time.Second).String())

	return output
}

func (fm *FlowMetrics) FlatRow() []int {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	output := []int{
		fm.id,
		fm.events,
	}

	if !slices.Contains(fm.skipColumns, "Pubkeys") {
		output = append(output, fm.pubkeys)
	}

	if !slices.Contains(fm.skipColumns, "Kinds") {
		output = append(output, len(fm.kinds))
	}

	output = append(output, fm.failures)

	if !slices.Contains(fm.skipColumns, "Synced") {
		output = append(output, fm.synced)
	}

	output = append(output, fm.retries)

	output = append(output, int(time.Since(fm.createdAt).Truncate(time.Second).Seconds()))

	return output
}

func (fm *FlowMetrics) GetAttributes() FlowAttr {
	return FlowAttr{
		Index:     fm.id,
		Name:      fm.name,
		FlagColor: fm.color,
	}
}

func (fm *FlowMetrics) IncreaseFailures() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.failures++
}

func (fm *FlowMetrics) IncreaseLost() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.lost++
}

// Lost exposes the raw permanent-loss count directly, since it no longer has
// its own FlatRow column (folded into the Failures cell in Row/FlatRow).
func (fm *FlowMetrics) Lost() int {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	return fm.lost
}

func (fm *FlowMetrics) IncreaseRetries(nextRetry time.Time) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.retries++
	fm.nextRetry = nextRetry
}

func (fm *FlowMetrics) AddEvent(kind int, pubkey string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.trackDiversity {
		fm.kinds[kind]++

		// Track unique pubkeys without unbounded growth
		if _, seen := fm.pubkeysSeen[pubkey]; !seen {
			fm.pubkeysSeen[pubkey] = struct{}{}
			fm.pubkeys++

			// Periodically clear the seen map to prevent unbounded growth
			// This gives approximate unique count which is acceptable for metrics
			if len(fm.pubkeysSeen) > 10000 {
				fm.pubkeysSeen = make(map[string]struct{})
			}
		}
	}

	fm.events++
	fm.eoseCounter++
}

func (fm *FlowMetrics) IncSynced() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.synced++
}

func (fm *FlowMetrics) EOSECount() int {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	return fm.eoseCounter
}

func (fm *FlowMetrics) ResetEOSECounter() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.eoseCounter = 0
}

func (fm *FlowMetrics) Close() {
	fm.closeFunc()
}

/////////

type FlowMetricsSlice []FlowStat

func (fm *FlowMetricsSlice) Sort(column *Column) {

	asc := column.SortDir == SortAsc

	sort.Slice((*fm), func(i, j int) bool {

		rowI := (*fm)[i].FlatRow()
		rowJ := (*fm)[j].FlatRow()

		if asc {
			return rowI[column.Index] < rowJ[column.Index]
		} else {
			return rowI[column.Index] > rowJ[column.Index]
		}
	})
}

func (fm *FlowMetricsSlice) Get(rowID int) FlowStat {
	if rowID < 0 || rowID >= len(*fm) {
		return nil
	}

	return (*fm)[rowID]
}

func (fm *FlowMetricsSlice) Neighbor(rowID int) (int, FlowStat) {
	if rowID < 0 || len(*fm) == 0 {
		return 0, nil
	}

	if rowID < len(*fm) {
		return rowID, (*fm)[rowID]
	}

	rowID -= 1
	if rowID < len(*fm) {
		return rowID, (*fm)[rowID]
	}

	return 0, nil
}

func (fm *FlowMetricsSlice) Remove(rowID int) {
	if rowID < 0 || rowID >= len(*fm) {
		return
	}

	(*fm)[rowID].Close()
	*fm = append((*fm)[:rowID], (*fm)[rowID+1:]...)
}

/////////

type InboundMetrics struct {
	*FlowMetrics
}

func NewInboundMetrics(id int, name string, closeCallback func()) *InboundMetrics {
	// "Synced" (an event the other side already had) is a destination-only
	// concept -- a source is only ever read from, so skip the column.
	// Pubkeys/Kinds diversity is tracked here since each source is a
	// distinct origin with its own diversity; see NewOutboundMetrics for why
	// destinations don't track the same thing.
	return &InboundMetrics{
		FlowMetrics: NewFlowMetrics(id, name, tcell.ColorGreen, []string{"Synced"}, true, closeCallback),
	}
}

/////////

type OutboundMetrics struct {
	*FlowMetrics
}

func NewOutboundMetrics(id int, name string, closeCallback func()) *OutboundMetrics {
	// Keep "Synced": a destination that's already fully synced from an
	// earlier run will show zero for every other counter (nothing new to
	// write, nothing rejected) -- Synced is the only column that
	// distinguishes "caught up" from "stuck."
	//
	// Skip Pubkeys/Kinds (and the trackDiversity=false below skips the
	// underlying bookkeeping too, not just the column): every destination
	// mirrors the same merged event stream from all sources, so these
	// numbers would be near-identical across every destination row and
	// against Sources -- diversity only varies meaningfully per source.
	return &OutboundMetrics{
		FlowMetrics: NewFlowMetrics(id, name, tcell.ColorBlue, []string{"Pubkeys", "Kinds"}, false, closeCallback),
	}
}

/////////

func formatTimestamp(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func renderFlag(color tcell.Color, index int, width int) string {
	indexStr := strconv.Itoa(index)
	padding := width - len(indexStr)
	if padding < 0 {
		padding = 0
	}
	leftPadding := padding

	return fmt.Sprintf("[-:%s:b]%s-%s-[-:-:-]", color, strings.Repeat("-", leftPadding), indexStr)
}
