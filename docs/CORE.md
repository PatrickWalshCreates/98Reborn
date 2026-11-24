# Modernizing the core

The goal: Windows 98 SE's own architecture, made stable and current on today's PCs. Every device has a
driver, Device Manager shows no yellow triangles, and everything runs crisply and fast. NVMe
([NVME.md](NVME.md)), booting ([BOOT.md](BOOT.md)) and 3D ([GPU.md](GPU.md)) have their own plans.
This page covers the other limits in 98's core.

## The north-star test: no yellow triangles

"Everything just works" becomes a test the lab runs automatically:
- **What it does:** after an install, a small tool inside Windows 98 asks the Configuration Manager for
  every device's status and problem code. A yellow triangle in Device Manager corresponds to a non-zero
  problem code.
- **When it runs:** in the QEMU lab on every change, and on each machine in the real-hardware lab.
- **How it's used:**
  - a machine's grade only reaches Gold when it passes this test
  - the compatibility report lists each device that would still show a problem, so users know before
    they install

## Storage limits

| Limit | Cause | Fix |
|---|---|---|
| 128 GiB per disk (2^28 sectors) | The built-in IDE driver (ESDI_506.PDR) uses 28-bit addressing | Our own disk port driver with 48-bit LBA. It shares its design with the NVMe driver: one modern storage stack, three front ends (AHCI, NVMe, legacy IDE) |
| ~127 GB file system tools | ScanDisk and Defrag in 98 fail on large FAT32 volumes | Ship fixed tools (or replacements), and keep the installer's default partition at 32 GB |
| FDISK misreports disks over 64 GB | A bug in 98's FDISK | Not needed: the installer partitions disks itself |
| 2 TiB per disk | MBR partition tables use 32-bit sector numbers | Can't be lifted for booting. Data beyond it could be reached later through a GPT-aware driver |

Lifting the 128 GiB limit also removes the planner's biggest restriction: Windows 98 could then go
anywhere on a large disk, including next to a big Windows or Linux partition.

## Memory limits

| Limit | Cause | Fix |
|---|---|---|
| Crashes or "insufficient memory" above ~1 GB | The disk cache (VCACHE) and the memory manager size their tables from total RAM | Today: the `ram-cap` and `vcache-tuning` modules cap what 98 sees. Next: patch the memory manager to use up to ~3.5 GB safely |
| 4 GB address space | 98 is a 32-bit system without PAE | Memory above 4 GB as a fast RAM disk or disk cache, through a PAE-aware driver. Games and apps then load from memory |

## CPU

| Area | Today | Plan |
|---|---|---|
| Fast-CPU bugs | The network driver's speed test divides by zero above ~2 GHz; the memory manager's TLB invalidation bug crashes 98 on newer CPUs | **Done:** the `cpu-speed-fix` module (in both editions) patches NDIS.VXD and the disk drivers, and extracts VMM from VMM32.VXD to apply the TLB fix. Patterns from patcher9x (MIT) |
| Timers and power | 98 predates HPET, modern ACPI and C-states, so CPUs run hot, and timing is off on some boards | Idle (HLT) fixes, ACPI table workarounds, and a stable timer source |
| SSE / modern instructions | 98 SE saves SSE registers on task switches; AVX and newer need XSAVE support the kernel doesn't have | Kernel support for saving extended state, so newer compilers' code and libraries run safely. Software written for 98 rarely needs more than SSE2, so this matters for new tools, not old programs |
| More than one core | 98 schedules a single CPU | Out of scope for the 98 kernel itself. Turbo mode ([GPU.md](GPU.md)) uses the other cores on the host side |

## Order

1. **Fast-CPU fixes:** needed for real hardware at all.
2. **48-bit LBA:** built as part of the NVMe storage stack.
3. **Memory up to ~3.5 GB.**
4. **The device-status test in the lab:** the "no yellow triangles" scoreboard.
5. **Above 4 GB as RAM disk / cache.**
6. **Extended CPU state.**
