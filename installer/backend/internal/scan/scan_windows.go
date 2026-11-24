//go:build windows

package scan

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/win9x-modern/w9xm/internal/compat"
)

// One PowerShell query; no admin rights needed (Secure Boot is reported as
// unknown when the check isn't allowed).
const psScan = `$ErrorActionPreference='SilentlyContinue'
$o=[ordered]@{}
$o.pci=@(Get-CimInstance Win32_PnPEntity | Where-Object { $_.DeviceID -like 'PCI\VEN_*' } | ForEach-Object { [ordered]@{id=$_.DeviceID; name=$_.Name; compat=@($_.CompatibleID)} })
$o.ram=(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory
$c=Get-CimInstance Win32_Processor | Select-Object -First 1
$o.cpu=$c.Name; $o.mhz=$c.MaxClockSpeed; $o.arch=$c.Architecture
$o.fw=(Get-ItemProperty 'HKLM:\System\CurrentControlSet\Control').PEFirmwareType
try { $o.sb=Confirm-SecureBootUEFI } catch { $o.sb=$null }
$o.os=(Get-CimInstance Win32_OperatingSystem).Caption
$o.disks=@(Get-PhysicalDisk | ForEach-Object { [ordered]@{name=$_.FriendlyName; bus=[string]$_.BusType; size=$_.Size} })
$o | ConvertTo-Json -Depth 5 -Compress`

func run() (*compat.Scan, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psScan).Output()
	if err != nil {
		return nil, fmt.Errorf("PowerShell scan failed: %w", err)
	}
	s, err := ParseWindows(out)
	if err != nil {
		return nil, fmt.Errorf("reading scan results: %w", err)
	}
	if s.Arch == "" {
		s.Arch = runtime.GOARCH
	}
	return s, nil
}
