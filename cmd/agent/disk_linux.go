//go:build linux

package main

import "syscall"

func diskPercent(path string) float64 {
	var stats syscall.Statfs_t
	if syscall.Statfs(path, &stats) != nil || stats.Blocks == 0 {
		return 0
	}
	return clamp(100 * (1 - float64(stats.Bavail)/float64(stats.Blocks)))
}
