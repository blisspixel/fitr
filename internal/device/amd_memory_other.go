//go:build !linux

package device

func amdMemoryCapacity(_ string, _ float64, vram float64, source string) (float64, string) {
	return vram, source
}
