# 3D on modern GPUs

Today a modern GPU in Windows 98 is a plain framebuffer: a sharp 2D desktop, no 3D acceleration. The last
Windows 98 drivers stop at GeForce 7 (NVIDIA) and Radeon X850 (ATI). Modern PCs are far too powerful to
be held to that. This plan gets real 3D on them, in four tracks ordered by how soon they deliver.

## Track 1: native resolution 2D on any GPU (baseline)

A Windows 98 display driver for the firmware framebuffer: VBE modes on CSM PCs, and the GOP framebuffer
exposed through [98Reborn Boot](BOOT.md) on UEFI PCs.
- Widescreen and native panel resolutions, 32-bit color.
- Builds on existing VBE display-driver work for 98. This is what the Silver grade promises today.

**Status (alpha.7):** the installer ships the VESA driver from
[vmdisp9x](https://github.com/JHRobotics/vmdisp9x) (MIT) as the `display-vesa` module. Windows picks it for
any graphics adapter with no Windows 98 driver of its own: the installer writes the adapter's exact PCI ID
into `98RVESA.INF`, so Windows installs it silently, and the desktop starts at 1024x768 in 32-bit colour.
Verified in QEMU (standard VGA). Still to do: native panel resolutions (the driver's mode list is fixed;
`vesamode.exe` can enumerate the video BIOS's modes) and the GOP framebuffer path on UEFI PCs.

## Track 2: fast software 3D on any GPU

One core of a 2024 CPU is roughly 50–100× a 1999 Pentium II. That's enough to render most late-90s
games in software at 640×480–1024×768.
- **How:** a software implementation of Direct3D (5–9), OpenGL and Glide that compiles its pixel
  pipelines to SSE/AVX code at run time, like Mesa's llvmpipe. SoftGPU and the Mesa ports for Windows
  9x already show this works on 98.
- **Our work:**
  - package and tune it
  - make it the default 3D path in the Gaming edition
  - publish game-by-game frame rates on the site
- **Limit:** Windows 98 runs on one core, so very heavy games (2002–2004) will be slow.

## Track 3: Turbo mode (the modern GPU, through a thin host)

Windows 98 runs on a tiny built-in hypervisor (the same Linux + KVM the installer already uses). It
boots straight to the 98 desktop in full screen, so the user never sees the layer underneath. 3D calls
(Glide, OpenGL, Direct3D) are forwarded to the real GPU, using the passthrough approach proven by
QEMU-3dfx and virtio-gpu/VirGL.
- **Every modern GPU works,** using the host's current NVIDIA, AMD or Intel driver. Games run with
  real hardware 3D at high resolutions and frame rates.
- **It also solves other hard problems:**
  - USB 3, NVMe, Wi-Fi and HD audio all work, because Linux drives them and 98 sees simple virtual
    devices
  - the CPU speed problems of fast PCs go away (the VM can be throttled)
- **Trade-off:** 98 isn't running directly on the hardware. Some software that needs direct hardware
  access (old dongles, ISA-era timing tricks, some machine-control cards) needs the native install
  instead.
- **Offered as a choice:** at install time on Gaming machines: Native (bare metal) or Turbo.
- **The lab already does most of it:** unattended 98 in QEMU, display, input. The new parts are the
  GPU forwarding drivers for 98 and a polished boot-to-desktop host.

## Track 4: native drivers for modern GPUs (research)

A real Windows 98 display and Direct3D driver for current GPUs, from the ground up. This is the moonshot:
- **AMD and Intel** publish register documentation and have open Linux drivers (amdgpu/radeonsi,
  i915/iris) to learn from. NVIDIA is far harder (reverse-engineered docs only).
- **Scope:**
  - mode setting
  - memory management for gigabytes of VRAM through 98's VxD model
  - a command-submission layer
  - a Direct3D 6–9 / OpenGL implementation that targets it, likely by adapting Mesa's Gallium drivers
- **Effort:** realistically a multi-year, single-vendor effort. Start with one AMD generation once
  NVMe and Turbo mode ship.

## Recommendation

1. **Now:** Track 1 (already part of Silver) and Track 2 benchmarks in the lab.
2. **Next GPU milestone:** Turbo mode prototype. It's the fastest way to real 3D on every modern GPU,
   and it reuses the installer's Linux base and the lab's QEMU control.
3. **Later:** Track 4 as an open research track, AMD first.

## What changes in grading

- **Turbo mode:** shown as its own line in the compatibility report: "3D: hardware (Turbo mode)" vs
  "3D: software". When it ships, a modern GPU no longer limits a Gaming PC to Silver.
- **Native mode:** the existing rules stay.
