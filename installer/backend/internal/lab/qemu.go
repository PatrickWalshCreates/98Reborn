// Package lab installs Windows 98 SE unattended in QEMU from the user's own
// CD image, and boots patched copies to verify modules inside a real guest.
package lab

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// VM is a running QEMU instance with a QMP control connection and a serial log.
type VM struct {
	cmd       *exec.Cmd
	qmp       net.Conn
	qmpR      *bufio.Reader
	mu        sync.Mutex
	SerialLog string
	done      chan struct{}
	exitErr   error
}

type VMConfig struct {
	QEMU       string // qemu-system-i386 binary
	Disk       string // raw disk image (IDE primary master)
	Floppy     string // optional floppy image (A:)
	BootFloppy bool   // boot A: once, then the hard disk on every reset
	MemoryMB   int
	Show       bool   // open a window instead of running headless
	LogDir     string // serial.log and qemu.log go here
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// FindQEMU locates qemu-system-i386.
func FindQEMU(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, n := range []string{"qemu-system-i386", "qemu-system-x86_64"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errors.New("QEMU not found: install it (macOS: brew install qemu; Linux: your package manager; Windows: qemu.org) or pass --qemu")
}

func StartVM(c VMConfig) (*VM, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if c.MemoryMB == 0 {
		c.MemoryMB = 256
	}
	serial := c.LogDir + "/serial.log"
	os.Remove(serial)
	args := []string{
		// No local APIC: Windows 98 is PIC-only, and with QEMU's APIC present its
		// startup masks LINT0, cutting the PIT timer off from the CPU, so the
		// boot hangs in a real-mode speed-calibration loop at the logo.
		"-machine", "pc", "-cpu", "pentium2,-apic", "-m", fmt.Sprint(c.MemoryMB),
		"-drive", "file=" + c.Disk + ",format=raw,if=ide,index=0,cache=writeback",
		"-vga", "cirrus", "-nic", "none", "-rtc", "base=localtime",
		"-serial", "file:" + serial,
		"-qmp", fmt.Sprintf("tcp:127.0.0.1:%d,server=on,wait=off", port),
		"-name", "w9xm-lab",
	}
	if c.Floppy != "" {
		args = append(args, "-drive", "file="+c.Floppy+",format=raw,if=floppy,index=0")
	}
	if c.BootFloppy {
		args = append(args, "-boot", "once=a,order=c")
	} else {
		args = append(args, "-boot", "order=c")
	}
	if !c.Show {
		args = append(args, "-display", "none")
	}
	log, err := os.Create(c.LogDir + "/qemu.log")
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(log, "%s %s\n\n", c.QEMU, strings.Join(args, " "))
	cmd := exec.Command(c.QEMU, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	vm := &VM{cmd: cmd, SerialLog: serial, done: make(chan struct{})}
	go func() { vm.exitErr = cmd.Wait(); log.Close(); close(vm.done) }()

	var conn net.Conn
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		select {
		case <-vm.done:
			return nil, fmt.Errorf("QEMU exited at startup (see %s/qemu.log): %v", c.LogDir, vm.exitErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if conn == nil {
		vm.Kill()
		return nil, fmt.Errorf("cannot connect to QEMU monitor: %v", err)
	}
	vm.qmp, vm.qmpR = conn, bufio.NewReader(conn)
	if _, err := vm.qmpR.ReadString('\n'); err != nil { // greeting
		vm.Kill()
		return nil, err
	}
	if _, err := vm.QMP("qmp_capabilities", nil); err != nil {
		vm.Kill()
		return nil, err
	}
	return vm, nil
}

// QMP runs a monitor command and returns its "return" value.
func (vm *VM) QMP(cmd string, args map[string]any) (json.RawMessage, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	req := map[string]any{"execute": cmd}
	if args != nil {
		req["arguments"] = args
	}
	b, _ := json.Marshal(req)
	vm.qmp.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := vm.qmp.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := vm.qmpR.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var resp struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Desc string `json:"desc"`
			} `json:"error"`
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.Event != "" {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("qmp %s: %s", cmd, resp.Error.Desc)
		}
		return resp.Return, nil
	}
}

func (vm *VM) Screenshot(path string) error {
	_, err := vm.QMP("screendump", map[string]any{"filename": path, "format": "png"})
	return err
}

// Serial returns everything the guest has written to COM1 so far.
func (vm *VM) Serial() string {
	b, _ := os.ReadFile(vm.SerialLog)
	return string(b)
}

func (vm *VM) Exited() <-chan struct{} { return vm.done }

// Shutdown asks the guest to power off (ACPI), then quits QEMU after grace.
func (vm *VM) Shutdown(grace time.Duration) {
	select {
	case <-vm.done:
		return
	default:
	}
	vm.QMP("system_powerdown", nil)
	select {
	case <-vm.done:
	case <-time.After(grace):
		vm.QMP("quit", nil)
		select {
		case <-vm.done:
		case <-time.After(10 * time.Second):
			vm.Kill()
		}
	}
}

func (vm *VM) Kill() {
	if vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
	}
	<-vm.done
}

// Attach connects to the QMP socket of an already-running QEMU (debug/recovery).
func Attach(port int, serialLog string) (*VM, error) {
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	vm := &VM{qmp: conn, qmpR: bufio.NewReader(conn), SerialLog: serialLog, done: make(chan struct{})}
	if _, err := vm.qmpR.ReadString('\n'); err != nil {
		return nil, err
	}
	if _, err := vm.QMP("qmp_capabilities", nil); err != nil {
		return nil, err
	}
	return vm, nil
}

// SendKeys presses one chord, e.g. SendKeys("alt", "a") or SendKeys("ret").
// Names are QEMU qcodes (ret, tab, spc, esc, alt, shift, ctrl, a..z, 0..9, minus).
func (vm *VM) SendKeys(keys ...string) error {
	var ks []map[string]string
	for _, k := range keys {
		ks = append(ks, map[string]string{"type": "qcode", "data": k})
	}
	_, err := vm.QMP("send-key", map[string]any{"keys": ks, "hold-time": 80})
	time.Sleep(150 * time.Millisecond)
	return err
}

// TypeText types letters, digits, space and '-' (enough for keys and names).
func (vm *VM) TypeText(s string) error {
	for _, r := range strings.ToLower(s) {
		var k []string
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			k = []string{string(r)}
		case r == '-':
			k = []string{"minus"}
		case r == ' ':
			k = []string{"spc"}
		default:
			return fmt.Errorf("TypeText: unsupported character %q", r)
		}
		if err := vm.SendKeys(k...); err != nil {
			return err
		}
	}
	return nil
}
