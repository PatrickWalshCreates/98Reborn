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

Windows 98 only understands MBR partition tables. UEFI firmware boots from MBR disks too, and starts
`\EFI\BOOT\BOOTX64.EFI` from a FAT partition, so 98Reborn Boot lives **on the Windows 98 partition
itself**. No extra partition is needed:

```
C:\EFI\BOOT\BOOTX64.EFI      98Reborn Boot (the menu)
C:\EFI\98REBORN\REBIOS.EFI   BIOS layer (needs VT-x/AMD-V + IOMMU)
C:\EFI\98REBORN\CSMWRAP.EFI  BIOS layer for PCs without them
C:\EFI\98REBORN\BOOT.CFG     menu settings
```

On GPT disks shared with Windows 11 or Linux, see the multi-boot section in TOOL.md for the options.

## The BIOS layer: ReBIOS first, CSMWrap as the fallback

- **ReBIOS** (GPL-2.0) runs SeaBIOS and the legacy OS under a minimal hypervisor (VT-x/AMD-V plus an
  IOMMU), and adds back what modern PCs dropped:
  - ACPI tables that Windows 98 understands, so it installs with ACPI (power button, fans, thermal limits)
  - a complete emulated VGA with VESA modes up to the screen's resolution, even on graphics with no
    video BIOS
  - virtual 8259 and xAPIC interrupt controllers
  - a clean CPU state at handoff

  It's the successor to CSMWrap, by the same authors, and our default.
- **CSMWrap** (LGPL-2.1) puts SeaBIOS directly on the hardware. It's deprecated, but it needs no
  virtualization, so it's the fallback for PCs without VT-x or an IOMMU.
- **Installation:** both are shipped unmodified, pinned by SHA-256 (`internal/baseimage/vendor.go`),
  with a NOTICE of their licenses and sources. The installer puts them in `\EFI\98REBORN` on the
  Windows 98 partition. ReBIOS returns to the menu when the PC can't run it, and the menu then tries
  CSMWrap.
- **NVMe:** the NVMe INT 13h work in [NVME.md](NVME.md) lands in SeaBIOS, which both layers run.

## The menu (`installer/boot`)

A small UEFI program in C (built with clang and lld, no EFI SDK: `src/efi.h` has the definitions it
uses):
- **Look:** draws the 98Reborn look on the firmware's framebuffer with the Terminus font. Falls back to
  text if there's no graphics output.
- **Entries:** Windows 98, plus Windows Boot Manager and Linux loaders (`shim`, GRUB, systemd-boot) it
  finds on every disk. Then firmware setup and power off.
- **Settings:** in `\EFI\98REBORN\BOOT.CFG`: `timeout`, `default`, and `engine` (auto, rebios,
  csmwrap).
- **Firmware entry:** on UEFI PCs the installer adds a "98Reborn Boot" firmware boot entry
  (`efibootmgr`) and leaves other systems' entries alone.

## Tested so far (QEMU + OVMF with no CSM, i.e. a "class 3" UEFI PC)

| Check | Result |
|---|---|
| Menu draws, finds Windows Boot Manager and an Ubuntu loader on a second disk | Works |
| Windows 98 entry starts ReBIOS, which declines (no VT-x in plain QEMU) and returns | Works |
| Fallback starts CSMWrap, SeaBIOS boots the disk's MBR, "Starting Windows 98..." | Works |
| Windows 98 reaches the desktop under CSMWrap in QEMU | **No:** `IO.SYS` stops early (A20 stays off, the vector table gets overwritten). Real CPUs since Haswell ignore A20, so this may be emulation-only; needs a real PC |
| Windows 98 under ReBIOS (Nephrite: VT-x + Intel IOMMU, q35, OVMF without CSM) | Gets through DOS into Windows. **Logged boot reaches the desktop** in normal mode. A plain boot stops with a "Windows protection error"; it also reaches the desktop when NDIS.VXD isn't loaded |

**Open:** the protection error depends on layout and timing (logging or one driver fewer hides it).
It's not the RAM size, the USB emulation, the q35 chipset or the CPU model (each tested). The prime
suspect is the Windows 98 TLB invalidation bug that patcher9x fixes in VMM. Next step: extract
`VMM.VXD` from `VMM32.VXD` (W4/DoubleSpace container), apply the TLB patch, and save it as
`SYSTEM\VMM32\VMM.VXD`. The `cpu-speed-fix` module (NDIS speed test) is in place but alone doesn't
fix this.

## Milestones

| | Milestone | Status |
|---|---|---|
| **B0** | Menu + both BIOS layers, built in CI, installed by the installer | Done |
| **B1** | Windows 98 to the desktop on UEFI-only firmware (ReBIOS under Nephrite, then real PCs) | Next |
| **B2** | NVMe boot through the same SeaBIOS (NVME.md N1) | Planned |
| **B3** | 3+ real UEFI-only PCs (Intel and AMD, desktop and laptop) | Planned |
