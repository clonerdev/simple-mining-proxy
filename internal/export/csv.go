// Package export writes reports as CSV that spreadsheet programs open safely.
package export

import (
	"encoding/csv"
	"io"
	"strings"
)

// Safe neutralizes a cell that a spreadsheet would run as a formula. A text
// that starts with = + - @ (or a tab or carriage return, which some programs
// skip before looking) gets a leading apostrophe. Numbers are written by the
// caller from numeric values and never pass through here.
func Safe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// Write writes header and rows as CSV with a UTF-8 byte order mark, so Excel
// reads Persian and Russian text correctly. Every text cell goes through Safe.
func Write(w io.Writer, header []string, rows [][]string) error {
	if _, err := io.WriteString(w, "\uFEFF"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	line := func(cells []string) error {
		out := make([]string, len(cells))
		for i, c := range cells {
			out[i] = Safe(strings.ReplaceAll(c, "\x00", ""))
		}
		return cw.Write(out)
	}
	if err := line(header); err != nil {
		return err
	}
	for _, r := range rows {
		if err := line(r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
