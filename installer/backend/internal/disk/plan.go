package disk

import (
	"fmt"
	"sort"
	"strings"
)

// Windows 98 limits the planner respects.
const (
	// MaxEnd: without a 48-bit LBA driver, Windows 98 reaches only the first
	// 2^28 sectors (128 GiB) of a disk, so its partition must end below that.
	MaxEnd = int64(1) << 28
	// MinSize is the smallest partition offered (Setup plus room to work).
	MinSize = int64(2<<30) / Sector
	// WantSize is the default size: plenty for 98 and its software, and well
	// inside what 98's own disk tools handle.
	WantSize = int64(32<<30) / Sector
	align    = 2048 // 1 MiB
	// When shrinking another system, it keeps this much free space beyond
	// its minimum (or 15% of its size, whichever is larger).
	keepFree = int64(10<<30) / Sector
)

// Option kinds.
const (
	KindWholeDisk = "whole-disk"
	KindFree      = "free-space"
	KindShrink    = "shrink"
)

// Option is one place Windows 98 could be installed.
type Option struct {
	Kind        string
	Disk        *Disk
	Title       string
	Detail      string
	Recommended bool
	Available   bool
	Reason      string // why it isn't available

	Start, Sectors int64 // the new Windows 98 partition
	Slot           int   // MBR slot (0-3) for the new entry
	Shrink         *Part // partition to shrink first, if any
	ShrinkTo       int64 // its new length in sectors
	Erases         []*Part
}

// Requirements tune the planner.
type Requirements struct {
	AllowNVMe  bool // developer override until the NVMe driver ships
	WantSize   int64
	LBA48Ready bool // a 48-bit LBA driver is installed: lift the 128 GiB limit
}

func alignUp(n int64) int64   { return (n + align - 1) / align * align }
func alignDown(n int64) int64 { return n / align * align }

// diskBlock says why Windows 98 can't use a disk at all ("" = it can).
func diskBlock(d *Disk, r Requirements) string {
	switch {
	case d.IsMedium:
		return "this is the installer medium"
	case d.LogicalSec != 0 && d.LogicalSec != Sector:
		return fmt.Sprintf("this disk uses %d-byte sectors; Windows 98 needs 512-byte sectors", d.LogicalSec)
	case d.Bus == "usb" || d.Removable:
		return "Windows 98 can't start from USB or removable disks"
	case d.Bus == "nvme" && !r.AllowNVMe:
		return "NVMe: Windows 98 support for NVMe is still in development"
	case d.Sectors() < MinSize+align:
		return "the disk is too small"
	}
	return ""
}

// important reports whether a partition holds something a user would miss.
func important(p *Part) bool {
	return !p.Extended && (p.OS != "" || p.FS != "" && p.FS != "swap")
}

// Plan lists every option across all disks, best first.
func Plan(disks []*Disk, r Requirements) []Option {
	if r.WantSize == 0 {
		r.WantSize = WantSize
	}
	limit := MaxEnd
	if r.LBA48Ready {
		limit = 1 << 32 // MBR's own limit (2 TiB)
	}
	var out []Option
	for _, d := range disks {
		if why := diskBlock(d, r); why != "" {
			if !d.IsMedium {
				out = append(out, Option{Kind: KindWholeDisk, Disk: d, Title: diskTitle(d), Reason: why})
			}
			continue
		}
		out = append(out, wholeDisk(d, r, limit))
		if d.Table == TableMBR {
			out = append(out, freeSpace(d, r, limit)...)
			out = append(out, shrinks(d, r, limit)...)
		}
		if d.Table == TableGPT && hasImportant(d) {
			out = append(out, Option{Kind: KindFree, Disk: d, Title: "Next to the systems on " + diskTitle(d),
				Reason: "this disk uses GPT partitioning, which Windows 98 can't read. Use a separate disk; sharing a GPT disk (hybrid MBR) comes in a later installer build"})
		}
	}
	// Recommendation: an empty whole disk, else free space, else a shrink.
	rank := func(o Option) int {
		switch {
		case !o.Available:
			return 9
		case o.Kind == KindWholeDisk && len(o.Erases) == 0:
			return 0
		case o.Kind == KindFree:
			return 1
		case o.Kind == KindShrink:
			return 2
		default:
			return 3 // erases data
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	if len(out) > 0 && out[0].Available && rank(out[0]) < 3 {
		out[0].Recommended = true
	}
	return out
}

func diskTitle(d *Disk) string {
	m := d.Model
	if m == "" {
		m = "disk"
	}
	return fmt.Sprintf("%s (%s %s, %s)", d.Name, human(d.Size), strings.ToUpper(d.Bus), m)
}

func hasImportant(d *Disk) bool {
	for _, p := range d.Parts {
		if important(p) {
			return true
		}
	}
	return false
}

func size(r Requirements, avail int64) int64 {
	return alignDown(min(r.WantSize, avail))
}

func wholeDisk(d *Disk, r Requirements, limit int64) Option {
	o := Option{Kind: KindWholeDisk, Disk: d, Slot: 0, Start: align}
	o.Sectors = size(r, min(d.Sectors(), limit)-align)
	for _, p := range d.Parts {
		if important(p) {
			o.Erases = append(o.Erases, p)
		}
	}
	o.Available = o.Sectors >= MinSize
	if len(o.Erases) == 0 {
		o.Title = "Use the whole of " + diskTitle(d)
		o.Detail = fmt.Sprintf("A %s Windows 98 partition on an empty disk. Nothing else is touched.", human(o.Sectors*Sector))
	} else {
		var what []string
		for _, p := range o.Erases {
			what = append(what, p.Describe())
		}
		o.Title = "Erase " + diskTitle(d) + " and use it for Windows 98"
		o.Detail = "ERASES EVERYTHING on this disk: " + strings.Join(what, "; ") + "."
	}
	if rest := d.Sectors() - o.Start - o.Sectors; o.Available && rest > MinSize {
		o.Detail += fmt.Sprintf(" The other %s stays unallocated.", human(rest*Sector))
	}
	return o
}

// gaps lists unallocated regions (aligned) of an MBR disk.
func gaps(d *Disk) [][2]int64 {
	type iv struct{ s, e int64 }
	var used []iv
	for _, p := range d.Parts {
		if p.Num <= 4 { // logical partitions live inside their extended one
			used = append(used, iv{p.Start, p.End()})
		}
	}
	sort.Slice(used, func(i, j int) bool { return used[i].s < used[j].s })
	var out [][2]int64
	cur := int64(align)
	end := min(d.Sectors(), 1<<32)
	for _, u := range used {
		if s, e := alignUp(cur), alignDown(u.s); e-s >= MinSize {
			out = append(out, [2]int64{s, e})
		}
		cur = max(cur, u.e)
	}
	if s, e := alignUp(cur), alignDown(end); e-s >= MinSize {
		out = append(out, [2]int64{s, e})
	}
	return out
}

func freeSlot(d *Disk) int {
	used := [4]bool{}
	for _, p := range d.Parts {
		if p.Num >= 1 && p.Num <= 4 {
			used[p.Num-1] = true
		}
	}
	for i, u := range used {
		if !u {
			return i
		}
	}
	return -1
}

func freeSpace(d *Disk, r Requirements, limit int64) []Option {
	if !hasImportant(d) {
		return nil // the whole-disk option covers an empty disk
	}
	slot := freeSlot(d)
	var out []Option
	for _, g := range gaps(d) {
		o := Option{Kind: KindFree, Disk: d, Slot: slot, Start: g[0]}
		o.Title = fmt.Sprintf("Free space on %s (%s)", diskTitle(d), human((g[1]-g[0])*Sector))
		switch {
		case slot < 0:
			o.Reason = "all four primary partition slots are in use"
		case g[0]+MinSize > limit:
			o.Reason = "this free space is beyond 128 GB, the most of a disk Windows 98 can reach"
		default:
			o.Sectors = size(r, min(g[1], limit)-g[0])
			o.Available = true
			o.Detail = fmt.Sprintf("A new %s Windows 98 partition in unused space. Existing partitions aren't changed.", human(o.Sectors*Sector))
		}
		out = append(out, o)
	}
	return out
}

func shrinks(d *Disk, r Requirements, limit int64) []Option {
	slot := freeSlot(d)
	var out []Option
	for _, p := range d.Parts {
		if p.Num > 4 || !important(p) {
			continue
		}
		o := Option{Kind: KindShrink, Disk: d, Shrink: p, Slot: slot}
		name := p.Describe()
		o.Title = "Make room next to the " + name
		// Space just after p, up to the next partition.
		next := min(d.Sectors(), 1<<32)
		for _, q := range d.Parts {
			if q.Num <= 4 && q.Start >= p.End() && q.Start < next {
				next = q.Start
			}
		}
		minKeep := int64(0)
		if p.MinBytes > 0 {
			used := (p.MinBytes + Sector - 1) / Sector
			minKeep = alignUp(used + max(keepFree, p.Sectors*15/100))
		}
		switch {
		case p.BitLocker:
			o.Reason = "BitLocker is on. Suspend or turn off BitLocker in Windows first, then run the installer again"
		case p.Hibernate:
			o.Reason = "Windows is hibernated or uses Fast Startup. In Windows, turn off Fast Startup and shut down fully, then run the installer again"
		case p.ShrinkReason != "":
			o.Reason = p.ShrinkReason
		case p.FS != "ntfs" && p.FS != "ext4":
			o.Reason = "the installer can't shrink " + fsName(p.FS) + " partitions"
		case p.MinBytes == 0:
			o.Reason = "couldn't check how far this partition can shrink"
		case slot < 0:
			o.Reason = "all four primary partition slots are in use"
		case p.End() > limit+align:
			o.Reason = fmt.Sprintf("Windows 98 can only reach the first 128 GB of a disk, so this would mean shrinking it to under 128 GB. Use a separate disk for Windows 98 instead")
		}
		if o.Reason != "" {
			out = append(out, o)
			continue
		}
		// The new partition goes at the end of the space p can give up (plus
		// any gap after it), below the reach limit.
		lo := alignUp(p.Start + minKeep)
		hi := alignDown(min(next, limit))
		start := max(lo, alignDown(hi-r.WantSize))
		o.Start, o.Sectors = start, max(0, hi-start)
		if o.Sectors < MinSize {
			if p.Start+minKeep+MinSize > limit {
				o.Reason = "Windows 98 can only reach the first 128 GB of a disk, and this partition can't shrink below that line"
			} else {
				o.Reason = "not enough free space inside this partition"
			}
			o.Sectors, o.Start = 0, 0
			out = append(out, o)
			continue
		}
		o.ShrinkTo = min(p.Sectors, o.Start-p.Start)
		o.Available = true
		o.Detail = fmt.Sprintf("Shrinks it from %s to %s (its files are kept), then adds a %s Windows 98 partition after it.",
			human(p.Sectors*Sector), human(o.ShrinkTo*Sector), human(o.Sectors*Sector))
		if o.ShrinkTo == p.Sectors {
			o.Shrink, o.Kind = nil, KindFree
			o.Detail = fmt.Sprintf("A new %s Windows 98 partition in unused space after it.", human(o.Sectors*Sector))
		}
		out = append(out, o)
	}
	return out
}

// Layout draws a disk as text, before or after an option.
func Layout(d *Disk, o *Option) string {
	type seg struct {
		start, n int64
		label    string
	}
	var segs []seg
	for _, p := range d.Parts {
		if p.Num > 4 {
			continue
		}
		if o != nil && o.Kind == KindWholeDisk {
			break
		}
		n := p.Sectors
		label := p.Describe()
		if o != nil && o.Shrink == p {
			n = o.ShrinkTo
			label += " (shrunk to " + human(n*Sector) + ")"
		}
		segs = append(segs, seg{p.Start, n, label})
	}
	if o != nil {
		segs = append(segs, seg{o.Start, o.Sectors, "NEW: Windows 98 (" + human(o.Sectors*Sector) + " FAT32)"})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].start < segs[j].start })
	var b strings.Builder
	cur := int64(0)
	for _, s := range segs {
		if s.start-cur > align*16 {
			fmt.Fprintf(&b, "  [ %s free ]\n", human((s.start-cur)*Sector))
		}
		fmt.Fprintf(&b, "  [ %s ]\n", s.label)
		cur = s.start + s.n
	}
	if rest := d.Sectors() - cur; rest > align*16 {
		fmt.Fprintf(&b, "  [ %s free ]\n", human(rest*Sector))
	}
	return b.String()
}
