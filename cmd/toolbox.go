package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/toolbox"
	"github.com/EasySBTeam/EasySB/internal/toolbox/tools"
)

// runToolboxTool prints one toolbox entry as a plain table and exits. It is the same run
// the panel starts from its menu, for a host that is scripted rather than opened, and it is
// how this project verifies a tool on a real machine.
func runToolboxTool(id string, lang i18n.Lang) {
	if id == "list" || id == "" {
		printToolboxList(lang)
		return
	}
	tool, ok := tools.Lookup(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown toolbox entry %q\n\n", id)
		printToolboxList(lang)
		os.Exit(2)
	}

	fmt.Printf("%s\n\n", lang.T("toolbox_"+id))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := tool.Run(ctx, toolbox.Options{Log: func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }})
	// The run is written down whatever it produced, so a result measured without a terminal
	// shows up in the panel's 看板 as well. A board that cannot be written is not a failed
	// run: the table below is what the caller asked for, and it is printed either way.
	if recordErr := tools.Record(id, result, err); recordErr != nil {
		fmt.Fprintf(os.Stderr, "board: %v\n", recordErr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", lang.T("toolbox_failed"), err)
		os.Exit(1)
	}
	printPlainTable(lang, result)
}

// printPlainTable writes a result without the panel's frame: this output is read from a log
// or piped into something else, so it stays plain text and keeps its columns aligned.
func printPlainTable(lang i18n.Lang, result toolbox.Result) {
	headers := result.Headers
	if len(headers) == 0 {
		headers = []string{"item", "value"}
	}
	rows := make([][]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		localised := make([]string, 0, len(row))
		for _, cell := range row {
			localised = append(localised, cellText(lang, cell))
		}
		rows = append(rows, localised)
	}
	localisedHeaders := make([]string, 0, len(headers))
	for _, header := range headers {
		localisedHeaders = append(localisedHeaders, cellText(lang, header))
	}

	// Column widths are display widths, not rune counts: a Chinese verdict occupies two
	// columns per character, and counting runes would leave every table with a Chinese
	// cell ragged.
	width := make([]int, len(headers))
	for i, header := range localisedHeaders {
		width[i] = lipgloss.Width(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(width) && lipgloss.Width(cell) > width[i] {
				width[i] = lipgloss.Width(cell)
			}
		}
	}
	printRow := func(cells []string) {
		line := ""
		for i, w := range width {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			line += cell + strings.Repeat(" ", w-lipgloss.Width(cell)+2)
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	printRow(localisedHeaders)
	for _, row := range rows {
		printRow(row)
	}

	for _, note := range result.Notes {
		fmt.Println("· " + note)
	}
	if result.Summary != "" {
		fmt.Println()
		fmt.Println(result.Summary)
	}
}

// cellText words the tokens the panel defined, so the command line reads like the panel's
// table: the verdicts first, then the column names the registry uses.
func cellText(lang i18n.Lang, cell string) string {
	if tools.IsVerdict(cell) {
		return tools.Verdict(lang, cell)
	}
	switch cell {
	case "service":
		return lang.T("toolbox_col_service")
	case "status":
		return lang.T("toolbox_col_status")
	case "region":
		return lang.T("toolbox_col_region")
	case "item":
		return lang.T("toolbox_col_item")
	case "value":
		return lang.T("toolbox_col_value")
	}
	return cell
}

// printToolboxList names every entry, so an unknown --tool argument is answered with the
// list instead of an error alone.
func printToolboxList(lang i18n.Lang) {
	for _, group := range tools.Groups() {
		fmt.Println("[" + lang.T("toolbox_group_"+group) + "]")
		for _, tool := range tools.InGroup(group) {
			fmt.Printf("  %-18s %s\n", tool.ID, lang.T("toolbox_"+tool.ID))
		}
	}
}
