# 98Reborn

A modern, network-aware installer that puts a patched, driver-equipped **Windows 98 SE** onto current PCs,
plus a built-in **Control Center** for specs, monitoring and updates. Core and Gaming editions.

**Bring your own Windows:** this project ships no Microsoft code. You supply your own 98 SE CD/ISO and key;
the installer patches it locally.

- Plan & architecture: [docs/PLAN.md](docs/PLAN.md) · NVMe driver plan: [docs/NVME.md](docs/NVME.md) · Tool and installer: [docs/TOOL.md](docs/TOOL.md) · UEFI boot: [docs/BOOT.md](docs/BOOT.md) · 3D on modern GPUs: [docs/GPU.md](docs/GPU.md) · Modernizing the core (storage, memory, CPU): [docs/CORE.md](docs/CORE.md)
- **98Reborn Tool** (`installer/backend/cmd/98reborn`): one app for Windows, macOS and Linux that opens in
  your browser, checks the PC it's on, and builds the bootable USB installer from your own 98 SE CD. Try it
  from source: `cd installer/backend && go run ./cmd/98reborn`.
- **PC compatibility scanner:** `98reborn-check` (Windows 10/11 or Linux, no admin rights, nothing is changed
  or sent anywhere) grades a PC Gold / Silver / Bronze / In development / Not supported, with the reasons
  why. Build it with `installer/backend/scripts/build-all.sh`. The rules are data:
  [rules.json](installer/backend/internal/compat/rules.json).

| Path | What |
|---|---|
| `installer/` | Linux-based live installer: live env, Go backend, web UI |
| `modules/` | Patch/fix modules (deltas, ini/registry edits) |
| `drivers/` | Signed driver manifest keyed by PCI/USB ID |
| `control-center/` | Win32 management app that runs inside 98 SE |
| `editions/` | Core and Gaming module lists |
| `site/` | GitHub Pages website |
| `tests/` | QEMU/86Box install tests, hardware compatibility reports |
