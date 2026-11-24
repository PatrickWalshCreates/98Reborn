# 98Reborn Boot: starting Windows 98 on UEFI PCs

Windows 98 boots through the legacy BIOS: the MBR, the boot sector, `IO.SYS`, and BIOS services
(INT 13h disk, INT 10h video, INT 16h keyboard) the whole way to the desktop. Many PCs from 2020 on
have UEFI firmware only, with no legacy BIOS ("CSM") to provide those services. **98Reborn Boot** is our
UEFI boot program that fills that gap, under the 98Reborn name and look.

## What it does

1. The UEFI firmware starts `\EFI\98Reborn\BOOTX64.EFI` (98Reborn Boot) from a small EFI partition.
2. It shows the 98Reborn boot screen, plus a menu if other operating systems are installed (see
   [Multi-boot](TOOL.md#installing-alongside-other-operating-systems)).
3. For Windows 98, it installs a legacy BIOS **inside** the UEFI environment (based on SeaBIOS, through
   the approach CSMWrap pioneered):
   - BIOS data areas and interrupt vectors
   - INT 13h disk services
   - INT 10h video on top of the firmware's GOP framebuffer
   - legacy USB keyboard and mouse
4. It then loads the MBR / boot sector of the Windows 98 partition, exactly as a BIOS would. From
   there, Windows 98 boots normally.
5. For other entries (Windows 10/11, Linux) it simply starts their own UEFI boot loader, unchanged.

On PCs that still have CSM, 98Reborn Boot isn't required. The installer then uses classic BIOS boot,
and the same menu is available as boot-sector code.

## Disk layout

Windows 98 only understands MBR partition tables. UEFI firmware can boot from MBR disks too: it looks
for a partition of type `0xEF` (EFI System). A disk set up for 98 on a UEFI-only PC therefore looks
like this:

| # | Type | Size | Contents |
|---|---|---|---|
| 1 | `0x0C` FAT32 (LBA), active | 8–127 GB | Windows 98 (`C:`) |
| 2 | `0xEF` EFI System (FAT) | 64 MB | 98Reborn Boot, branding, settings |

Windows 98 ignores partition 2. On GPT disks shared with Windows 11 or Linux, see the multi-boot
section in TOOL.md for the options.

## Why build on SeaBIOS / CSMWrap

- **SeaBIOS is mature.** It's the BIOS in QEMU, used across countless VMs and coreboot boards.
- **CSMWrap** already shows that SeaBIOS can run as a UEFI application on real class-3 machines.
- **One codebase for NVMe boot.** The NVMe INT 13h work in [NVME.md](NVME.md) (milestone N1) lands in
  the same SeaBIOS that 98Reborn Boot ships. Fixing NVMe boot once fixes it for both QEMU and real UEFI PCs.

Before we fork, we'll check the license of each component and keep upstream attribution.

## Milestones

| | Milestone | Done when |
|---|---|---|
| **B0** | Prototype | Builds in CI; boots the lab's golden 98 SE image from an MBR disk with an ESP in QEMU + OVMF (no CSM) |
| **B1** | Branded | 98Reborn boot screen and menu; settings file on the ESP; quiet by default |
| **B2** | Installer integration | Installer creates the ESP, installs 98Reborn Boot, and registers it in UEFI boot entries without touching other OSes' entries |
| **B3** | NVMe | Boots 98 from NVMe via our INT 13h path (NVME.md N1) |
| **B4** | Real hardware | Boots on 3+ UEFI-only PCs (Intel and AMD, desktop and laptop) |
