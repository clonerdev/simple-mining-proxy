package export

import (
	"strings"
	"testing"
)

func TestSafeNeutralizesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1":      "'=1+1",
		"+cmd|calc": "'+cmd|calc",
		"-5":        "'-5",
		"@SUM(A1)":  "'@SUM(A1)",
		"\tx":       "'\tx",
		"plain":     "plain",
		"":          "",
		"س-ام":      "س-ام",
	} {
		if got := Safe(in); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteAddsBOMAndQuotes(t *testing.T) {
	var sb strings.Builder
	err := Write(&sb, []string{"name", "note"}, [][]string{{"a,b", "=HYPERLINK(\"x\")"}, {"фарм", "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.HasPrefix(got, "\uFEFFname,note\n") {
		t.Fatalf("no BOM or header: %q", got)
	}
	if !strings.Contains(got, `"a,b"`) || !strings.Contains(got, `"'=HYPERLINK(""x"")"`) || !strings.Contains(got, "фарм,ok") {
		t.Fatalf("csv = %q", got)
	}
}
