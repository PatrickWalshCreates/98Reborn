package lines

import (
	"testing"

	"github.com/win9x-modern/w9xm/internal/textfile"
)

func TestBlockLifecycle(t *testing.T) {
	d := textfile.Parse([]byte("@ECHO OFF\r\nSET BLASTER=A220\r\n"))
	if !EnsureBlock(d, "reg", []string{"ECHO hi"}, "start") {
		t.Fatal("expected insert")
	}
	if EnsureBlock(d, "reg", []string{"ECHO hi"}, "start") {
		t.Fatal("expected no-op")
	}
	if !EnsureBlock(d, "reg", []string{"ECHO bye"}, "start") {
		t.Fatal("expected replace")
	}
	want := "REM W9XM BEGIN reg\r\nECHO bye\r\nREM W9XM END reg\r\n@ECHO OFF\r\nSET BLASTER=A220\r\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q", got)
	}
	if !RemoveBlock(d, "reg") || string(d.Bytes()) != "@ECHO OFF\r\nSET BLASTER=A220\r\n" {
		t.Fatalf("remove failed: %q", d.Bytes())
	}
}

func TestEdit(t *testing.T) {
	d := textfile.Parse([]byte("DEVICE=EMM386.EXE\r\nREM DEVICE=EMM386.EXE\r\nREM W9XM BEGIN x\r\nDEVICE=EMM386.EXE\r\nREM W9XM END x\r\n"))
	changed, err := Edit(d, `^\s*device\s*=.*emm386`, "comment")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	want := "REM DEVICE=EMM386.EXE\r\nREM DEVICE=EMM386.EXE\r\nREM W9XM BEGIN x\r\nDEVICE=EMM386.EXE\r\nREM W9XM END x\r\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q", got)
	}
}
