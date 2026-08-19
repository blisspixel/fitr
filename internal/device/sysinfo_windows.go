//go:build windows

package device

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func ps(script string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	exe := filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	out, err := exec.Command(exe, "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gpuInfo() (name, driver, date string) {
	raw := ps(`Get-CimInstance Win32_VideoController | Select-Object -First 1 ` +
		`Name,DriverVersion,@{n='DriverDate';e={$_.DriverDate.ToString('yyyy-MM-dd')}} ` +
		`| ConvertTo-Json -Compress`)
	var d struct {
		Name          string `json:"Name"`
		DriverVersion string `json:"DriverVersion"`
		DriverDate    string `json:"DriverDate"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return "", "", ""
	}
	return d.Name, d.DriverVersion, d.DriverDate
}

func cpuName() string {
	return ps(`(Get-CimInstance Win32_Processor | Select-Object -First 1).Name`)
}

func ramGB() float64 {
	n, err := strconv.ParseInt(ps(`(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory`), 10, 64)
	if err != nil {
		return 0
	}
	return float64(n) / GB
}
