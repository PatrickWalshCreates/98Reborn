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
4. **Choose:** edition (Core / Gaming) and where to install: a whole disk, free space, or space shrunk
   from an existing partition next to other operating systems (see below).
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
| **ISO writer** (ISO 9660 + Joliet + El Torito, BIOS and UEFI) | Done |
| Base installer image (Alpine Linux, kernel 6.18 LTS, QEMU, installer), built by `98reborn-mkbase` locally or in CI | Done: boots BIOS and UEFI, CD and USB |
| Installer first stage: drivers, find medium + `98SE/`, internet (optional, warned), grade, list disks | Done (text mode) |
| Compose base + your CD into the installer ISO (`w9xm make-installer`) | Done (Tool UI next) |
| Smart partitioner (multi-boot): detect systems, plan, shrink NTFS/ext4, add the partition | Done (text mode) |
| Install to the real disk: Setup in a VM on a scratch image, patch, copy only used data into place | Done |
| Tool UI (local web UI opened in the browser; one binary per OS) | Next |
| Installer UI (the same web UI in a kiosk browser on the live system) | Next |
| Hybrid USB boot (isohybrid MBR/GPT) and a built-in USB writer | Later |

## Building the base image

`98reborn-mkbase` builds the base image from Alpine Linux packages without Docker or Alpine tools, so it
runs on macOS, Windows and Linux:
- **Verified downloads:** the package index is checked against Alpine's signing keys, pinned in
  `internal/apk/keys`. Every package is checked against the signed index.
- **Small:** only the needed kernel drivers (storage, USB, input, Ethernet, file systems, KVM) and only
  QEMU's PC firmware go in. The initramfs is about 51 MB, the ISO about 140 MB.
- **Boots both ways:** ISOLINUX for BIOS, and systemd-boot on a FAT32 EFI image for UEFI. On a USB stick
  written by Rufus (ISO mode), UEFI uses the same files directly from the stick.

```
cd installer/backend
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/98reborn-installer-linux-amd64 ./cmd/98reborn-installer
go run ./cmd/98reborn-mkbase --installer dist/98reborn-installer-linux-amd64 --out 98reborn-base.iso
go run ./cmd/w9xm make-installer --base 98reborn-base.iso --cd "Windows 98 Second Edition.iso" --out 98Reborn.iso
```

CI (`.github/workflows/installer-base.yml`) builds the base image on every change, boot-tests it in
QEMU, and attaches it to tagged releases.

## Installing alongside other operating systems

Windows 98 can share a PC with Windows 10/11, Linux or other systems. The installer's partitioner plans
this for the user, and changes nothing until they confirm.

**What it detects:**
- each disk and its partition table (MBR or GPT)
- installed systems: Windows (NTFS with `\Windows\System32`), Linux (ext4/btrfs/xfs with `os-release`),
  older DOS/9x installs, and other FAT/exFAT data
- free space
- whether partitions can be shrunk safely

**Its choices, best first:**
1. **A disk of its own** (a second SSD/HDD, or an empty one): cleanest and recommended. It doesn't touch
   any other disk.
2. **Free space on an MBR disk:** adds a primary FAT32 partition.
3. **Shrink a partition on an MBR disk:** NTFS (ntfsresize) or ext4 (resize2fs), checked first, then
   adds the FAT32 partition in the freed space.
4. **GPT disk shared with another OS:** Windows 98 can't read GPT. The options are a separate disk
   (recommended), or a *hybrid MBR* that mirrors the 98 partition. A hybrid MBR is shown as advanced,
   with a clear warning, because other OSes' disk tools can undo it.

**How the install touches the disk:** Windows Setup never sees the real disk. It runs in a VM against a
sparse scratch image whose partition has exactly the position and size of the planned one. After the
edition's modules are applied, any shrink happens. Only the used parts of the finished partition are then
copied into place, and the new partition entry is written last. Other partitions, the MBR boot code and
the gap after the MBR (where GRUB lives) are never written, except when the user chose to erase the whole
disk.

**Windows 98's own rules it applies:**
- primary FAT32 partition
- 32 GB by default, 2 GB minimum
- the partition must end within the first 128 GB of the disk, until 98Reborn's 48-bit LBA driver ships
  ([CORE.md](CORE.md))
- it should be the first FAT partition on the disk, so that it becomes `C:`

**Safety rules:**
- **No resize when the partition isn't safe to change:**
  - BitLocker is on: suspend or decrypt it in Windows first
  - Windows is hibernated or has Fast Startup on: shut Windows down fully first
  - the file system has errors
- **Backups:** partition tables are saved to the installer medium before any change.
- **Confirmation:** the plan is shown as a before/after picture of the disk, and must be confirmed.

**Booting:**
- **The menu:** [98Reborn Boot](BOOT.md) shows a menu with every system found. Windows 98 starts
  through its built-in BIOS layer. Windows 10/11 and Linux start through their own boot loaders,
  unchanged.
- **Boot entries:** the installer adds its own UEFI boot entry and never edits other systems' entries.
  The user chooses which one is the default.
- **Legacy BIOS PCs:** the same menu runs as boot-sector code.

## Why a local web UI

The Tool must look the same on Windows, macOS and Linux and ship as a single file. A small local web server
inside the Go binary, opened in the user's browser, gives a modern interface with no GUI toolkit and no
installer. The live installer reuses the same UI in a full-screen browser.
