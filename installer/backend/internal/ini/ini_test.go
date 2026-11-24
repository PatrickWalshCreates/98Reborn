package ini

import "testing"

const sys = "[boot]\r\nshell=Explorer.exe\r\n\r\n[386Enh]\r\n; comment\r\nDEVICE=*vshare\r\nmaxphyspage = 1000\r\nMaxPhysPage=2000\r\n\r\n[vcache]\r\n\x1a"

func TestSetPreservesFormatting(t *testing.T) {
	f := Parse([]byte(sys))
	if !f.Set("386ENH", "MaxPhysPage", "40000") {
		t.Fatal("expected change")
	}
	want := "[boot]\r\nshell=Explorer.exe\r\n\r\n[386Enh]\r\n; comment\r\nDEVICE=*vshare\r\nmaxphyspage=40000\r\n\r\n[vcache]\r\n\x1a"
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if f.Set("386Enh", "maxphyspage", "40000") {
		t.Fatal("second set should be a no-op")
	}
}

func TestRoundTripUntouched(t *testing.T) {
	for _, s := range []string{sys, "a=b\n[x]\ny=z", "", "\r\n", "[s]\r\nk=v"} {
		if got := string(Parse([]byte(s)).Bytes()); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestInsertNewKeyAndSection(t *testing.T) {
	f := Parse([]byte(sys))
	f.Set("vcache", "MaxFileCache", "262144")
	f.Set("NewSec", "A", "1")
	if v, _ := f.Get("VCACHE", "maxfilecache"); v != "262144" {
		t.Fatal("vcache key not set")
	}
	want := "[boot]\r\nshell=Explorer.exe\r\n\r\n[386Enh]\r\n; comment\r\nDEVICE=*vshare\r\nmaxphyspage = 1000\r\nMaxPhysPage=2000\r\n\r\n[vcache]\r\nMaxFileCache=262144\r\n\r\n[NewSec]\r\nA=1\r\n\x1a"
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestInsertBeforeBlankLines(t *testing.T) {
	f := Parse([]byte("[a]\r\nx=1\r\n\r\n[b]\r\n"))
	f.Set("a", "y", "2")
	if got := string(f.Bytes()); got != "[a]\r\nx=1\r\ny=2\r\n\r\n[b]\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestAddAndDelete(t *testing.T) {
	f := Parse([]byte(sys))
	if f.Add("386Enh", "device", "*VSHARE") {
		t.Fatal("existing pair should not be re-added")
	}
	if !f.Add("386Enh", "device", "mydrv.vxd") {
		t.Fatal("expected add")
	}
	v := "mydrv.vxd"
	if !f.Delete("386Enh", "device", &v) {
		t.Fatal("expected delete")
	}
	if _, ok := f.Get("386Enh", "device"); !ok {
		t.Fatal("other device= line must survive")
	}
	if !f.Delete("386Enh", "maxphyspage", nil) {
		t.Fatal("expected delete all")
	}
	if _, ok := f.Get("386Enh", "maxphyspage"); ok {
		t.Fatal("key should be gone")
	}
}
