package tui

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestTable(t *testing.T) {

	app := NewApp().Init()

	db := 1

	rows := FlowMetricsSlice{}
	rows = append(rows, NewOutboundMetrics(1, fmt.Sprintf("/data/db/notes_%d", db), func() { app.Debug("deleted") }))
	rows = append(rows, NewOutboundMetrics(2, fmt.Sprintf("/data/db/notes_%d", db+1), func() { app.Debug("deleted") }))

	table := NewTable(app, "Test", &rows)
	table.Init(context.Background())

	app.Load(table)

	go func() {
		for {
			db += 2

			rows = append(rows, NewOutboundMetrics(1, fmt.Sprintf("/data/db/notes_%d", db), func() { app.Debug("deleted") }))
			rows = append(rows, NewOutboundMetrics(2, fmt.Sprintf("/data/db/notes_%d", db+1), func() { app.Debug("deleted") }))

			<-time.Tick(time.Second * 2)
		}
	}()

	go func() {
		for i := 5; i > 0; i-- {
			app.Debug(fmt.Sprintf("Closed in %d seconds...", i))
			time.Sleep(time.Second * 2)
		}
		app.Stop()
	}()

	app.Run()

}

func _TestTableSort(t *testing.T) {

	app := NewApp().Init()

	indexWidth := len(strconv.Itoa(10))

	rows := FlowMetricsSlice{}
	for i := 1; i < 10; i += 2 {
		a := NewOutboundMetrics(i, fmt.Sprintf("/data/db/notes_%d", i), func() { app.Debug("deleted") })
		a.SetIndexWidth(indexWidth)
		b := NewOutboundMetrics(i+1, fmt.Sprintf("/data/db/notes_%d", i+1), func() { app.Debug("deleted") })
		b.SetIndexWidth(indexWidth)
		rows = append(rows, a, b)
		time.Sleep(time.Second * 1)
	}

	table := NewTable(app, "Test", &rows)
	table.Init(context.Background())

	app.Load(table)

	app.Run()

}
