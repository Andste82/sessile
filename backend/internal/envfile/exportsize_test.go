package envfile

import (
	"errors"
	"strings"
	"testing"
)

// quote mirrors sshpty.shellSingleQuote.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestExportSizeMatchesRealQuoting(t *testing.T) {
	es := []Entry{
		{Name: "A", Value: "plain"},
		{Name: "QUOTES", Value: "it's 'x' ''"},
		{Name: "EMPTY"},
	}
	var b strings.Builder
	for _, e := range es {
		b.WriteString("export " + e.Name + "=" + quote(e.Value) + "; ")
	}
	want := len(quote(b.String()))
	if got := ExportSize(es); got != want {
		t.Errorf("ExportSize = %d, want %d", got, want)
	}
}

func TestValidateRefusesOverlyQuotedEnvironment(t *testing.T) {
	// 4 values of 4096 quotes each: within MaxTotalBytes (16 KiB), but
	// 13x once quoted twice, far over MaxExportBytes.
	v := strings.Repeat("'", MaxValueLen)
	es := []Entry{{Name: "A", Value: v}, {Name: "B", Value: v}, {Name: "C", Value: v}}
	err := Validate(es)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "quoted") {
		t.Fatalf("err = %v, want quoted-size ErrInvalid", err)
	}
	if strings.Contains(err.Error(), "''") {
		t.Error("error echoes a value")
	}
	if err := Validate(es[:1]); err != nil {
		t.Errorf("one entry should fit: %v", err)
	}
}
