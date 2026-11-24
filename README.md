# 98Reborn

A modern, network-aware installer that puts a patched, driver-equipped **Windows 98 SE** onto current PCs,
plus a built-in **Control Center** for specs, monitoring and updates. Core and Gaming editions.

**Bring your own Windows:** this project ships no Microsoft code. You supply your own 98 SE CD/ISO and key;
the installer patches it locally.

- Plan & architecture: [docs/PLAN.md](docs/PLAN.md) · NVMe driver plan: [docs/NVME.md](docs/NVME.md)
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
