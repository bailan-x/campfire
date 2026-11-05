//go:build !linux

package agent

// Non-Linux stubs: the Agent only runs on the game host (Linux). These exist so
// `go build ./...` works from the Windows dev machine.
func memInfo() (totalKB, usedKB, swapTotalKB int64) { return 0, 0, 0 }

func diskInfo(path string) (totalKB, usedKB int64) { return 0, 0 }
