package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Column struct {
	Index    int
	Name     string
	IsSorted bool
	SortKey  rune
	SortDir  string
}

func (c *Column) ToggleSort() {
	c.IsSorted = true
	if c.SortDir == SortAsc {
		c.SortDir = SortDesc
	} else {
		c.SortDir = SortAsc
	}
}

const (
	SortAsc  = "asc"
	SortDesc = "desc"
)

type Table struct {
	*tview.Table
	app   *App
	title string

	columns      []*Column
	rows         *FlowMetricsSlice
	scrollOnce   sync.Once
	setupColumns sync.Once

	sortedColumn *Column

	mu sync.Mutex
}

func NewTable(app *App, title string, data *FlowMetricsSlice) *Table {
	t := &Table{
		app:   app,
		Table: tview.NewTable(),
		title: title,
		rows:  data,
	}

	return t
}

func (t *Table) UpdateTitle(count int) {
	t.SetTitle(fmt.Sprintf(" [::b][%s]%s [[%s]%d[%s]] ", tcell.ColorPurple, strings.ToUpper(t.title), tcell.ColorWhiteSmoke, count, tcell.Color53))
}

func (t *Table) Init(ctx context.Context) *Table {

	t.SetFixed(1, 1).
		SetSelectable(true, false).
		SetBorder(true).
		SetBorderColor(tcell.ColorPurple).
		SetBorderPadding(0, 1, 1, 1)

	t.SetSelectedStyle(tcell.Style{}.
		Background(tcell.ColorPurple).
		Foreground(tcell.ColorWhite),
	)

	t.UpdateTitle(0)

	go t.render(ctx)

	return t
}

func (t *Table) handleKeys(event *tcell.EventKey) *tcell.EventKey {

	if event.Key() != tcell.KeyRune {
		return event
	}

	switch event.Rune() {
	case 'd':
		t.mu.Lock()
		defer t.mu.Unlock()

		rowIndex, _ := t.GetSelection()
		rowID := rowIndex - 1
		if row := t.rows.Get(rowID); row != nil {
			t.app.ConfirmDelete(fmt.Sprintf("Delete %s ?", row.GetAttributes().Name), func() {
				t.rows.Remove(rowID)
				if nID, n := t.rows.Neighbor(rowID); n != nil {
					t.Select(nID+1, 0)
				}
			})
		} else if rowID > 0 {
			t.app.Error(fmt.Sprintf("Row %d not found.", rowID))
		}

	default:
		var sortedColumn *Column
		for _, c := range t.columns {
			if event.Rune() == c.SortKey {
				sortedColumn = c
			}
		}

		if sortedColumn != nil {
			for _, c := range t.columns {
				c.IsSorted = false
			}
			sortedColumn.ToggleSort()
			t.sortedColumn = sortedColumn
			t.Update()
		}
	}

	return event

}

func (t *Table) DrawHeaders() {

	t.setupColumns.Do(func() {
		if len(*t.rows) == 0 {
			return
		}
		row := (*t.rows)[0]
		t.columns = row.Columns()

		t.SetInputCapture(t.handleKeys)

		for _, c := range t.columns {
			if c.IsSorted {
				t.sortedColumn = c
				break
			}
		}

	})

	for ci, c := range t.columns {
		header := fmt.Sprintf("[-:-:b]%s", strings.ToUpper(c.Name))
		if c.IsSorted {
			switch c.SortDir {
			case SortAsc:
				header += fmt.Sprintf("[%s::]↑", tcell.ColorPurple)

			default: // SortDesc
				header += fmt.Sprintf("[%s::]↓", tcell.ColorPurple)
			}
		}
		t.SetCell(0, ci,
			tview.NewTableCell(header).
				SetExpansion(1).
				SetTextColor(tcell.ColorBlack).
				SetAlign(tview.AlignLeft).
				SetSelectable(false))
	}

}

func (t *Table) Update() {
	if t.rows == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.Clear()

	t.UpdateTitle(len(*t.rows))
	t.DrawHeaders()

	if t.sortedColumn != nil {
		t.rows.Sort(t.sortedColumn)
	}

	for ri, r := range *t.rows {
		for c, column := range r.Row() {
			t.SetCell(ri+1, c, tview.NewTableCell(column).
				SetExpansion(1).
				SetAlign(tview.AlignLeft))
		}
	}

	t.scrollOnce.Do(func() {
		t.ScrollToBeginning()
	})
}

func (t *Table) render(ctx context.Context) {

	ticker := time.NewTicker(time.Second * 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.app.QueueUpdateDraw(func() {
				t.Update()
			})

		case <-ctx.Done():
			return
		}
	}
}
