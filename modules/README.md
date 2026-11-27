# modules

Patch modules (one dir each: manifest + binary deltas + ini/registry edits). Never commit Microsoft files.

Modules the installer always applies (besides the edition's):
- `installer-cleanup`: removes the Setup-finished hook.
- `new-hardware`: on the first real start, removes devices Windows Setup found in its virtual PC (Cirrus graphics, ports, monitor), so the PC's own hardware is set up instead.
- `display-vesa`: the VESA display driver from vmdisp9x (MIT) for graphics adapters without a Windows 98 driver.
- `usb3-xhci98`: the xHCI98 USB 3 controller driver (GPL-2.0-or-later; its source is attached to every release, from `SOURCE-URL.txt`). Windows asks once, at the first start, to install the controller; USB keyboards, mice and hubs then install silently.
