# Lab: automated Windows 98 SE install and module tests in QEMU

`w9xm lab` installs Windows 98 SE unattended in QEMU from **your own CD image**, then boots patched copies of
that install to prove modules work inside a real Windows 98: not just that files changed, but that Windows
still boots and the changes took effect.

It runs on macOS (Apple Silicon or Intel), Linux and Windows. All disk work (partitioning, FAT32, copying
setup files, patching) is done by `w9xm` itself, so there's no mounting, no root, and no third-party disk tools.
The only dependency is QEMU (`brew install qemu` on macOS).

## One-time: build the golden image

```bash
cd installer/backend && go build -o w9xm ./cmd/w9xm && cd ../..
installer/backend/w9xm lab install --iso "ISOs/Windows 98 Second Edition.iso" --accept-eula --out lab
```

- Your product key is read from the ISO's folder: `product-key.txt` or `Win98Key.rtf`/`.txt`. Plain text
  or RTF both work; the one `XXXXX-XXXXX-XXXXX-XXXXX-XXXXX` string in the file is used. `--key-file`
  overrides it. The key goes only into the VM's answer file, never into the repo.
- `--accept-eula` lets the harness select "I accept" on Setup's license page on your behalf. Without it,
  the run waits on that page for you to accept it yourself in a `--show` window.
- `--resume` continues an interrupted install on the existing `golden.img.partial`. `lab finalize`
  turns a finished partial image into `golden.img` if the harness was stopped after the guest said DONE.
- `--show` opens a QEMU window so you can watch. Otherwise it runs headless and keeps a fresh screenshot in
  `lab/install/screen.png`, plus one every 5 minutes (`screen-NNN.png`).
- Takes a while under emulation (x86 on Apple Silicon is emulated in software, not virtualized).
  The result is `lab/golden.img`, a clean, booted 98 SE install. `lab/` is git-ignored.

How it works:
1. A 2 GB raw disk gets an MBR, a FAT32 partition, the CD's `WIN98\` folder at `C:\WIN98`, an `MSBATCH.INF`
   answer file, and `C:\W9XM\READY.BAT`.
2. The CD's own El Torito boot floppy is extracted and rewritten. `JO.SYS` (the CD's "Boot from Hard
   Disk / CD-ROM" menu, which defaults to the hard disk) is removed so DOS loads directly. `CONFIG.SYS`
   loads `HIMEM` only, and `AUTOEXEC.BAT` runs `FDISK /MBR` → `SMARTDRV` →
   `SETUP C:\MSBATCH.INF /IS /IE /IM /IV`.
3. QEMU boots the floppy once (`-boot once=a,order=c`). Every reset after that boots the hard disk.
4. After the first reboot, 98 SE shows a second Setup Wizard (User Information, License Agreement, Product
   Key, Start Wizard) even with `Display=0`/`ShowEula=0` in `MSBATCH.INF`. The harness answers it. When the
   screen has been idle for 30 s (fewer than 300 changed pixels, so a blinking caret doesn't count), it
   fingerprints the page heading. A known page gets its keystrokes over QMP; anything else is left alone.
   The name, organisation and key are already pre-filled from the answer file.
5. The answer file registers `READY.BAT` to run at every Windows startup. It writes progress markers to COM1
   (captured as `serial.log`), runs an optional `CHECK.BAT`, then shuts Windows down.
6. Afterwards, `AutoScan=0` is set in `MSDOS.SYS` so a forced power-off never stalls the next boot on ScanDisk.

## Every change: test modules

```bash
installer/backend/w9xm lab test --edition gaming --out lab
installer/backend/w9xm lab test --module ram-cap --set ram-cap.ram_cap_mb=512 --out lab
```

Each run:
1. sparse-copies `golden.img` to `lab/runs/<timestamp>/disk.img`;
2. applies the modules to the image with the normal engine, journalled exactly as on a real machine;
3. writes `CHECK.BAT`, which has Windows export every registry key the modules set (`START /W REGEDIT /E`);
4. boots the image and waits for `READY`/`DONE`;
5. reads the image back and checks:
   - Windows reached the desktop with the modules applied;
   - real-mode REGEDIT imported `PENDING.REG` at boot (it was renamed to `APPLIED.REG`);
   - every queued registry value appears in Windows' own export;
   - every changed file survived a boot unchanged.

Exit status is non-zero if any check fails. Screenshots, `serial.log` and `qemu.log` stay in the run directory.

## Files

| Path | What |
|---|---|
| `lab/golden.img` | Clean install, reused by every test (never modified) |
| `lab/golden.json` | Product, release fingerprint, size, date |
| `lab/bootfloppy.img` | The rewritten CD boot floppy |
| `lab/install/` | Install-run screenshots and logs |
| `lab/runs/<ts>/` | One directory per test run: disk, screenshots, logs |

## Debugging a stuck run

- `go run ./cmd/vmprobe DISK FLOPPY OUTDIR 5 30 60` boots the images and saves screenshots at those
  seconds, plus the serial output.
- `dist/vmctl PORT shot FILE | keys alt+a ret | pause | resume | hmp info registers` drives a running lab VM
  (the QMP port is in the run's `qemu.log`). Stop the harness process first: QMP takes one client at a time.
- `go run ./cmd/fatcat DISK [PATH]` lists the root of, or prints a file from, a disk image (read-only),
  e.g. `SETUPLOG.TXT` or `BOOTLOG.TXT` while Setup is running.

Problems already found and fixed:
- **Idle detection:** comparing screenshot hashes never saw a wizard page as idle because of the blinking
  caret (13 pixels). It now compares pixels with a 300-pixel threshold.
- **Blind wizard steps went out of sync:** an unrelated slow screen took the first step. Pages are now
  recognized by their heading fingerprint. Reference screenshots for the recognizer test live in
  `lab/ref/` (git-ignored: Microsoft UI, and the key page shows your product key).
- **CD boot menu:** the CD's floppy loads `JO.SYS` ("Boot from Hard Disk / CD-ROM", default hard disk),
  so `AUTOEXEC.BAT` never ran. The harness removes `JO.SYS`.
- **Hang at the boot logo after Setup:** with QEMU's local APIC present, Windows 98's startup masks the
  APIC's LINT0. The PIT timer can then no longer reach the CPU, and the real-mode speed calibration loop
  (`CMP AX, ES:[046C]`) spins forever. A hard reset doesn't help. The VM CPU is now `pentium2,-apic`
  (98 is PIC-only anyway).

## Status (2026-11-23, 98 SE on QEMU 11.1, Apple M1 Pro)

- **A fully unattended install works:** `lab install --accept-eula` went from an empty disk to
  `golden.img` in 24m44s with no intervention. The stages: DOS → file copy (about 9 min) → wizard pages
  answered by recognition → hardware setup → restart (no APIC hang) → desktop → `READY.BAT` → clean
  shutdown.
- `lab test --edition gaming`: all checks pass. Windows boots with all five modules, real-mode REGEDIT
  imports `PENDING.REG` before Windows loads (including `HKCU` via `/R:USER.DAT`), and every patched file
  survives a boot.
- Registry deletes (the setupx INF run once via `RunOnce`) were verified with a throwaway module: the
  deleted value is gone and its sibling is kept, with escaping intact.
- A test boot takes about 20 s once the golden image exists.

## Notes and limits
- Guest hardware: i440FX, Pentium II, 256 MB, Cirrus VGA, no network (so no logon prompt). That's a
  known-good baseline. Real-hardware coverage comes from the hardware lab in Phase 0.
- QEMU is software-emulated on Apple Silicon, so installs are slow; tests reuse the golden image to stay fast.
