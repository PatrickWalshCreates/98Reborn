//go:build linux

package disk

import (
	"regexp"
	"strconv"
)

var (
	regexpSize   = regexp.MustCompile(`Current volume size: (\d+) bytes`)
	regexpBlocks = regexp.MustCompile(`Block count:\s+(\d+)`)
	regexpBS     = regexp.MustCompile(`Block size:\s+(\d+)`)
)

func atoi(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
