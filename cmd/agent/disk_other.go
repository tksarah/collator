//go:build !linux

package main

func diskPercent(string) float64 { return 0 }
