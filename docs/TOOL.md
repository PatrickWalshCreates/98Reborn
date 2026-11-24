# 98Reborn Tool and installer media

The **98Reborn Tool** is the one download most people need. It runs on the computer you have now
(Windows 10/11, macOS or Linux) and does two jobs:

1. **Check a PC:** grades the computer it's running on (Gold / Silver / Bronze / In development / Not
   supported) with reasons and fixes. That doesn't have to be the PC you'll install on; the installer
   checks the target again.
2. **Create installer media:** you pick your Windows 98 SE CD or ISO, and it writes a new bootable
   **98Reborn installer ISO**. Put that on a USB stick with Rufus (or the Tool's own writer, later) and boot
   the target PC from it.

## What the 98Reborn ISO contains

```
98Reborn-installer.iso   (built on your computer; never distributed)
├── boot/ efi/ ...       our open-source installer: a small Linux system plus the installer app
├── 98reborn/            modules, editions, driver catalog snapshot, the patch engine
└── 98SE/                the WIN98 setup folder copied from *your* CD (the only Microsoft content)
```

The base installer image (everything except `98SE/`) is open source and built by CI. It's published as a
release asset, and the Tool downloads it once and caches it. The Tool then writes a new ISO: base image +
your `98SE/` folder, with both boot records kept (BIOS and UEFI). One USB stick then holds the installer and
Windows. The installer finds `98SE/` on the medium it booted from.

## On the target PC

1. **Boot the USB stick** (BIOS or UEFI). The installer starts; no Windows 98 code runs yet.
2. **Connect to the internet** (Ethernet or Wi-Fi). It's optional. If you skip it, a warning says
   compatibility will be limited: no driver downloads, no fixes newer than the medium, possibly
   unsupported devices. Everything on the medium still works.
3. **Compatibility check** of this exact machine (the same rules as the Tool), shown as the grade with reasons,
   plus BIOS settings to change (CSM, SATA mode, Secure Boot).
4. **Choose:** edition (Core / Gaming), target disk, keep or replace partitions.
5. **Install:** partition and format the disk (our FAT32 code), copy `98SE/` to `C:\WIN98`, and run
   Windows 98 Setup unattended inside a minimal virtual machine with the real disk attached. This is the
   same approach the lab uses, so Setup always sees simple, known hardware.
6. **Patch:** apply the edition's modules and the drivers matched to this PC's devices (the patch engine,
   journalled), then reboot into Windows 98 on the real hardware.

## Building blocks

| Piece | Status |
|---|---|
| Hardware scan and grading (`compat`, `scan`) | Done (Windows, Linux) |
| Reading the user's CD (`iso9660`, `cab`, `media`) | Done |
| FAT32, partitioning, patching disk images (`fat`, `engine`) | Done |
| Unattended Setup in a VM + verification (`lab`) | Done, needs a "real disk" mode |
| **ISO writer** (ISO 9660 + Joliet + El Torito, BIOS and UEFI) | In progress |
| Base installer image (Linux, kernel, QEMU, installer app), built in CI | Next |
| Tool UI (local web UI opened in the browser; one binary per OS) | Next |
| Installer UI (the same web UI in a kiosk browser on the live system) | Next |
| Hybrid USB boot (isohybrid MBR/GPT) and a built-in USB writer | Later |

## Why a local web UI

The Tool must look the same on Windows, macOS and Linux and ship as a single file. A small local web server
inside the Go binary, opened in the user's browser, gives a modern interface with no GUI toolkit and no
installer. The live installer reuses the same UI in a full-screen browser.
