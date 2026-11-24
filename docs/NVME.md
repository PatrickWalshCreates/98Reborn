# NVMe support for Windows 98 SE

Most new PCs ship NVMe-only, so 98Reborn has to install and run on NVMe. No NVMe driver for Windows 9x
exists that we know of. This document is the plan for building one.

## Why it's two problems

Windows 98 touches the disk in two very different ways:

1. **Real mode (boot):** the boot sector, `IO.SYS` and the early Windows loader read the disk through the
   BIOS (`INT 13h`). NVMe drives normally have no legacy BIOS support, so this needs either firmware that
   provides it, or our own `INT 13h` handler.
2. **Protected mode (Windows running):** Windows' I/O Supervisor (IOS) talks to disks through *port
   drivers* (`.PDR`). Without one for NVMe, Windows falls back to "MS-DOS compatibility mode": it calls
   `INT 13h` from virtual-8086 mode. That's slow, but it works if the `INT 13h` handler is V86-safe.

So the path is: **make `INT 13h` work → Windows runs (slowly) in compatibility mode → native `NVME.PDR`
makes it fast.** Each stage is useful on its own.

## First experiment (2026-11-23, QEMU lab)

The golden 98 SE image was attached as a QEMU `-device nvme` drive, booting through SeaBIOS, which has its
own NVMe driver.
- The BIOS booted from NVMe. The MBR, boot sector and `IO.SYS` loaded, and the Windows 98 logo appeared.
- After 77 reads (211 KB) the drive went idle. The CPU loops between the BIOS and System Management Mode
  (`CS=A000`, `SMM=1`): SeaBIOS uses SMM to reach its 32-bit NVMe code from 16-bit callers.
- **This matters beyond QEMU.** CSMWrap, the legacy-boot workaround for UEFI-only PCs, *is* SeaBIOS, so
  this exact code path will run on real hardware. Fixing it, or replacing it, is milestone N1.

## Milestones

| | Goal | Done when |
|---|---|---|
| **N0** ✅ | Lab can boot 98 from an emulated NVMe drive | Logo reached from NVMe (above) |
| **N1** | Reliable `INT 13h` for NVMe | 98 boots to the desktop from NVMe in QEMU, in compatibility mode. Either fix the SeaBIOS SMM/call32 hang, or ship our own small open-source `INT 13h` NVMe handler, loaded by our boot code before `IO.SYS` (polled I/O, V86-safe, a single namespace) |
| **N2** | `NVME.PDR` read path | A native IOS port driver: controller init, admin and I/O queue pair, polled completion, PRP lists. Windows reports the disk as 32-bit (no compatibility mode) |
| **N3** | Read/write, interrupts, performance | Writes, flush, INTx/MSI interrupts, larger transfers. `lab test` passes on NVMe and the edition benchmark is close to SATA |
| **N4** | Real hardware | Works on drives from at least 3 vendors (e.g. Samsung, WD, Crucial) across Intel and AMD chipsets, booted via vendor CSM and via CSMWrap |
| **N5** | Installer | The installer offers NVMe targets, and the scanner rates NVMe-only PCs accordingly |

## Technical notes
- **32-bit address space:** 98 can't see memory above 4 GB. The controller's registers (BAR0) must be
  mapped below 4 GB, which CSM boot normally arranges. The scanner should warn when it can't confirm this.
- **Simple first:** one namespace, one I/O queue, polled completion, 4 KB pages. That's enough for N1/N2.
- **Toolchain:** write it in C plus a little assembly, built with Open Watcom or MinGW. Use only publicly
  documented VxD/IOS interfaces, and publish the result as open source.
- **Testing:** QEMU's NVMe device in the lab for every change (`lab test` gets an `--nvme` option), then a
  small set of real drives.
- **Prior art to study:** SeaBIOS's NVMe driver (LGPLv3) for the `INT 13h` side, and the published
  structure of community IOS port drivers, such as the AHCI drivers written for 98.
