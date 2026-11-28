# Project Plan: Windows 98 SE on Modern PCs

> Project name: **98Reborn** (deliberately without "Windows"; see §1.2).

## 0. Summary

Build a **modern, network-connected installer** that puts a patched, driver-equipped Windows 98 SE onto
current hardware, plus a **Control Center** app inside 98 for specs, monitoring, and updates. Ship it as
open-source tooling and a GitHub Pages site.

Two things shape everything else:

1. **We cannot distribute Windows 98 itself.** The ISO we ship has no Microsoft code in it. The user
   supplies their own 98 SE CD/ISO and key, and the installer patches it on their machine.
2. **"New PC" has hard limits.** Firmware (UEFI without CSM), storage (NVMe, AHCI-only), USB 3 (xHCI),
   audio (HD Audio), and especially **GPUs** are where Win98 breaks. The project succeeds or fails on
   drivers, not on the installer UI.

---

## 1. Key decisions (recommended)

### 1.1 98 SE, not Me
| | 98 SE | Me |
|---|---|---|
| Real-mode DOS boot (needed for DOS games, SBEMU, memory managers) | Yes | Removed (needs hacks) |
| Community patches and drivers (USP, NUSB, PATCHMEM, AHCI, HDA) | Most target 98 SE | Fewer |
| Stability reputation | Better | Worse |
| Me features worth having (System Restore, USB mass storage) | Can be backported or replaced | Built in |

**Pick 98 SE.** Me's only real advantage is built-in USB mass storage, and NUSB already covers that on 98 SE.

### 1.2 Legal model: bring your own Windows
- Hosting 98 SE ISOs, or modified Microsoft binaries, on GitHub is copyright infringement. Expect a DMCA
  takedown that can take the whole repo down with it.
- **What we ship:** a bootable *installer ISO* (Linux-based, all open source) plus *patch data*.
- **What the user provides:** their own 98 SE CD or ISO and product key, dropped onto the USB stick or
  selected during setup.
- Patches to Microsoft files are shipped as **binary deltas** (bsdiff/xdelta) or as rules for "replace
  this file with our clean-room rewrite." They are applied locally, and the patched files never live in
  our repo.
- **Decompiling:** only *targeted* reverse engineering of specific drivers, for interoperability (which
  US/EU law generally allows within limits). Publish our own **clean-room** replacements, never
  decompiled Microsoft source. Don't attempt a full OS decompile: it's legally toxic and unnecessary.
  The kernel works fine; the failures are in drivers and limits.
- **Third-party components** (R. Loew patches, KernelEx, NUSB, USP, nGlide, etc.) each have their own
  licence. Many are freeware but *not redistributable*. Keep a `LICENSES.md` matrix. Where we can't
  redistribute, the installer downloads from the original source at install time, with user consent.
- Trademark: don't use "Windows" or the Microsoft logo in the project name or branding. "For Windows 98 SE"
  as a description is fine.

### 1.3 Target hardware tiers
Be upfront with users. Publish a hardware compatibility list (HCL).

| Tier | Meaning | Example |
|---|---|---|
| **Gold** | Everything works, including 3D | Board with CSM and SATA IDE mode, a Win9x-supported PCIe GPU (GeForce 6/7 class), a supported NIC |
| **Silver** | Works; 2D video (VESA) or software 3D; some devices unsupported | Most 2015–2022 desktops with CSM |
| **Bronze** | Boots via CSMWrap and AHCI drivers; limited | Class-3 UEFI machines (no CSM), recent laptops |
| **Bronze / In development** | NVMe-only PCs (most new machines) | UEFI boot: runs in compatibility mode through 98Reborn Boot (Bronze). Legacy/CSM boot: blocked until tested. Native driver: see [NVME.md](NVME.md) |
| **Unsupported** | ARM | — |

**The GPU problem:** no modern NVIDIA, AMD, or Intel GPU has 9x drivers. Options:
- A universal VESA driver (2D, high resolutions, no acceleration): shipped as the `display-vesa` module (vmdisp9x, MIT).
- SoftGPU in software-rendering mode (Mesa). **Benchmark this on a fast modern CPU.** If it runs
  late-90s and early-2000s D3D and OpenGL games acceptably, it's the headline feature of the Gaming edition.
- Recommend a cheap period-compatible PCIe card for Gold tier.

---

## 2. Architecture

> **Host platforms:** every build-side tool (the `w9xm` patch engine and the future ISO builder) is pure Go with
> no dependencies, and ships for Windows, macOS (Apple Silicon + Intel) and Linux. Development happens on a Mac.

```
┌──────────────────────── Installer USB / ISO (ours, open source) ────────────────────────┐
│  Linux live env (Alpine/Buildroot) → kiosk browser (Cage + WebKit) → Installer UI          │
│  Go backend: hw detect · network · driver fetch · disk prep · patcher · deploy             │
│  /user/ ← user drops WIN98SE.ISO here (or picks it from any disk)                          │
└────────────────────────────────────────────────────────────────────────────────────────────┘
          │ fetch (HTTPS, signed manifests)
          ▼
┌──────── Driver & patch repository (GitHub Releases + manifest.json, ed25519-signed) ────────┐
│  PCI/USB ID → package · licence · checksum · tier · known issues                              │
└───────────────────────────────────────────────────────────────────────────────────────────────┘
          │ deploys
          ▼
┌──────── Target disk: MBR, FAT32 C: ──────────────────────────────────────────────────────────┐
│  Patched 98 SE + staged drivers + tweaks + Control Center (Win32 app) + edition extras          │
└───────────────────────────────────────────────────────────────────────────────────────────────┘
```

### 2.1 Pre-install environment (the "modern graphical installer")
Linux is the right base. It already has drivers for every modern NIC, Wi-Fi card, and storage device,
so "connect to the internet before installing" comes for free.

- **Base:** Alpine or Buildroot, booting both BIOS and UEFI. (UEFI boot of the *installer* is fine; only
  98 itself needs legacy boot.)
- **UI:** web UI (Svelte or React) in a Cage kiosk. You can develop it on a Mac in a normal browser
  against a mocked backend.
- **Backend:** one static Go binary. It handles:
  1. **Hardware scan:** `lspci -nn`, `lsusb`, `dmidecode`, CPU and RAM, firmware mode (CSM available?),
     SATA controller mode.
  2. **Compatibility report:** each device shown green/yellow/red with what will and won't work.
     **This screen is the most important UX in the project.**
  3. **Network:** Ethernet/Wi-Fi via NetworkManager or iwd.
  4. **Driver resolution:** match IDs against the signed manifest, then download packages.
  5. **Firmware guidance:** "Enable CSM / Legacy boot", "Set SATA to IDE mode if available", or offer
     CSMWrap.
  6. **Disk prep:** MBR partitioning, FAT32 primary (≤127 GB unless the 48-bit LBA patch is applied),
     optional dual-boot with an existing OS via a chainloader.
  7. **Deploy** (see 2.2).
- **"Build drivers for the machine":** compiling VxDs on the fly isn't realistic. What we *can* do is
  generate per-machine INF files, registry entries, and `system.ini` tuning from the detected hardware.
  That gets the same result.

### 2.2 Deploy strategy
**Option A (recommended): run 98 Setup inside QEMU, inside the installer.**
The Linux env passes the target disk through to QEMU with known-good virtual hardware (i440FX, IDE,
VGA) and runs fully unattended 98 SE setup (`MSBATCH.INF`). Setup always sees identical hardware, so it
is deterministic and fast. Afterwards, the backend:
- strips the virtual-hardware entries from `HKLM\Enum` and the INF cache,
- injects real-hardware drivers and patched files,
- writes tuning to `system.ini` and `win.ini`, and the registry (via `hivex` or our own `.reg` merge on
  first boot),
- reboots to real hardware, where 98's PnP finds devices and uses the staged drivers.

**Option B:** classic unattended setup on bare metal (DOS phase → Setup). Slower and more fragile on
modern hardware. Keep it as a fallback.

### 2.3 The patch layer (makes 98 stable on new hardware)
| Problem on modern HW | Fix |
|---|---|
| CPUs over ~2.1 GHz cause "Windows Protection Error" at boot | Patched NDIS.VXD / timing fix (included in the unofficial service pack, USP) |
| Over 512 MB RAM causes instability; over ~1.5 GB won't boot | `MaxPhysPage` in `[386Enh]` to cap RAM (default cap 1 GB), `[vcache] MaxFileCache`; optional PATCHMEM to use more |
| Disks over 137 GB | 48-bit LBA patch for ESDI_506.PDR, or cap the partition |
| AHCI-only SATA | AHCI driver (R. Loew's, licence permitting) or a clean-room replacement, *or* IDE mode |
| USB 2.0 storage | NUSB |
| USB 3.0 / xHCI | **No mature stack. Biggest driver-writing task in the project** (see §5) |
| ACPI quirks | Install with ACPI off by default; per-board ACPI profiles in the manifest |
| Video | VBEMP / SoftGPU / native driver for supported cards |
| HD Audio | Watler's HDA driver (check licence and status); SBEMU for DOS-mode Sound Blaster emulation |
| Network | Realtek/Intel cards with existing 9x drivers; curate the list |
| Modern apps and APIs | KernelEx (optional, off in Core) |
| HTTPS (TLS 1.2/1.3) | Bundle a BearSSL or wolfSSL-based helper; optional local TLS proxy (WebOne-style) |

Every fix is a **module** with a manifest: id, files, deltas, registry, ini edits, conflicts,
hardware conditions, licence. Editions are lists of modules.

### 2.4 Control Center (post-install app inside 98)
Native Win32, written in C. Build with **Open Watcom v2** or **MinGW-w64 i686** (CI checks that every
import exists in 98 SE's `KERNEL32` and `USER32`). No .NET, no KernelEx dependency.

- **System:** CPU (CPUID), RAM (real vs. capped), disks, PCI device tree with driver status, BIOS info.
- **Build info:** edition, version, applied modules, patch hashes.
- **Resource monitor:** CPU, memory, GDI/USER resource percentages (the classic 98 killer), disk.
- **Updates:** signed manifest over HTTPS (BearSSL), module install and rollback, driver updates by PCI ID.
- **Tweaks:** RAM cap, vcache, DOS-mode boot, startup items, game-mode profile.
- **Diagnostics:** collect logs (`BOOTLOG.TXT`, `SETUPLOG.TXT`, device list) into a zip. Opt-in HCL
  report to the project.
- **Backup:** snapshot `SYSTEM.DAT`/`USER.DAT` + ini files before every change. One-click restore.
  (A cheap replacement for System Restore.)

### 2.5 Editions
**Core: for the software you still depend on.** Aimed at businesses running legacy
software (machine control, lab instruments, point of sale, accounting, CAD) and at everyday users with an
old app that won't run on modern Windows, such as an early Photoshop. Priorities are stability,
predictability and getting data in and out:
- patched 98 SE, matched drivers, Control Center, stability fixes;
- the runtimes old business software expects (VB5/VB6, MFC/MSVCRT, older Jet/ODBC, DCOM);
- **legacy I/O on modern boards:** USB-to-serial and USB-to-parallel adapter drivers, and PCIe serial and
  parallel cards, for machines that talk RS-232 or LPT;
- **data exchange:** USB mass storage, a print-to-PDF printer, and file sharing with modern PCs (modern
  Windows disables the SMB1 protocol 98 uses, so ship a documented bridge);
- a "locked-down" profile: offline by default, no auto-run, scheduled backups through the Control Center.

**Gaming: for the games you grew up with.** Aimed at people who remember late-90s and early-2000s PC
gaming (3D racers, licensed movie tie-ins, kids' sports titles, space shooters) and want those games
running the way they remember. Core plus:
- DirectX 9.0c; 3D through a period GPU, SoftGPU or Glide/D3D wrappers depending on hardware;
- **DOS mode:** startup menu, HimemX/JemmEx, SBEMU (Sound Blaster on modern audio), CTMOUSE;
- **period-correct sound and music:** a General MIDI software synth for games that relied on MIDI;
- **speed control:** CPU throttling and frame limiters for games that run too fast on modern CPUs;
- **controllers:** USB gamepads and joysticks through DirectInput;
- **your CDs:** mount ISO images of discs you own, and play CD-audio soundtracks from them;
- KernelEx, codecs (Indeo, Cinepak, MPEG-1 for cutscenes), and per-game fixes (a curated list).

Both editions are the *same installer ISO*. The edition is a choice in the UI, so we ship and test one image.

### 2.6 Website
GitHub Pages (Astro or plain static) in `site/`:
- landing page, screenshots, download (installer ISO + checksums + signature),
- **hardware compatibility list**, generated from the manifest + user reports,
- install guide (BIOS settings, making the USB, where to put your 98 SE ISO),
- FAQ, legal/licences page, changelog.

---

## 3. Things you hadn't listed (but will need)

1. **UEFI-only firmware:** most Intel boards from 2020 on dropped CSM. CSMWrap (SeaBIOS as a CSM on UEFI)
   is the escape hatch. Test it early; it determines how "new" a PC we can claim to support.
2. **HTTPS from inside 98:** every modern server needs TLS 1.2+. The updater must bundle its own TLS
   library. The web browser story (retro browser + proxy) also needs a decision.
3. **Security:** 98 must never be directly exposed to the internet. Default the firewall/proxy guidance
   and say so loudly in the docs.
4. **Signed updates:** ed25519-sign manifests so the updater can't be fed malicious drivers.
5. **Automated testing:** QEMU and 86Box install runs in CI on every change, plus a small real-hardware
   lab (3–5 machines covering the tiers).
6. **Rollback everywhere:** installer writes a restore point before tuning; Control Center snapshots
   before every change.
7. **Multi-boot:** many users will want this alongside Windows 11 or Linux. The installer's smart
   partitioner and the 98Reborn Boot menu handle it: see [TOOL.md](TOOL.md#installing-alongside-other-operating-systems)
   and [BOOT.md](BOOT.md).
8. **Keyboard and mouse:** USB HID under DOS and 98 on xHCI-only boards relies on BIOS legacy USB
   emulation. If that's missing, input dies. Detect this in the compatibility report.
9. **Licence matrix and attribution** for every third-party module.
10. **Telemetry-free HCL reports:** opt-in and anonymised. They feed the website's compatibility list.
11. **A VM "edition":** the same installer works in VMware, VirtualBox, QEMU, or 86Box with SoftGPU.
    It's an easy win and a big audience.
12. **Docs for "how to find your 98 SE media legally"** (original CDs, MSDN archives you own, etc.).

---

## 4. Roadmap

| Phase | Goal | Done when |
|---|---|---|
| **0. Research & lab** (2–4 wks) | Validate the hardest unknowns on real machines | Matrix of 3–5 PCs: CSM/CSMWrap boot, AHCI, RAM cap, VESA, SoftGPU software-render benchmarks |
| **1. Patch engine** | Module format + applier (Go), run against a stock 98 SE ISO | Reproducible patched file set from a user ISO, with hashes |
| **2. Installer MVP** | Alpine live + backend + QEMU deploy (Option A), CLI first | Unattended install onto a real Silver-tier PC that boots and works |
| **3. Installer UI** | Web UI: welcome → hw report → network → disk → edition → progress | Non-technical user installs from USB without docs |
| **4. Driver repo** | Signed manifest, PCI ID matching, download at install time | Installer pulls the right drivers for the lab machines |
| **5. Control Center** | Win32 app v1: specs, build info, monitor, updates | Runs on stock + patched 98 SE; updates a module end to end |
| **6. Gaming edition** | Curated game stack + DOS mode | A test list of ~30 games runs on Gold and Silver machines |
| **7. Site & release** | GitHub Pages, HCL, signed releases, CI | v0.1 public beta |
| **Ongoing** | Drivers: xHCI (shipped: xHCI98, GPL), HD Audio (shipped: WDMHDA, MIT), AHCI, NVMe | Each ships as a module |

## 5. Big-risk items (attack first)
0. **NVMe:** most new PCs have no SATA port, so this is close to non-negotiable. It's a new driver
   project with its own plan: [NVME.md](NVME.md).
1. **USB 3 / xHCI driver:** needed on boards without USB 2 ports or legacy emulation. A real
   VxD/WDM driver project: start research in Phase 0 and check current MSFN community efforts first.
2. **3D on modern GPUs:** four tracks, from software 3D to Turbo mode (GPU passthrough through a
   thin host) to native drivers: [GPU.md](GPU.md).
3. **Booting on UEFI-only PCs:** our own branded loader, 98Reborn Boot, built on SeaBIOS/CSMWrap:
   [BOOT.md](BOOT.md).
4. **Core limits (storage, memory, CPU)** and the "no yellow triangles" test: [CORE.md](CORE.md).
5. **Licensing** of key community patches (especially R. Loew's, given he passed away in 2019).
   Contact rights holders early; plan clean-room replacements for anything we can't redistribute.

## 6. Open decisions
- ~~Public project name~~: decided, **98Reborn**.
- UI stack: web UI in kiosk (recommended) vs. Qt/QML.
- Minimum supported hardware era (sets how hard we push on CSMWrap, xHCI, and NVMe).
- Whether the Gaming edition requires a period GPU for "Gold," or treats software 3D as first-class.
- Licence for our code (GPLv3 for the installer and drivers? MIT for the Control Center?).

> Note: community project names and statuses above (USP, NUSB, KernelEx, SoftGPU, VBEMP, SBEMU, Watler's
> HDA, CSMWrap, R. Loew patches) were current to my knowledge. Re-verify each in Phase 0 before
> depending on it.
