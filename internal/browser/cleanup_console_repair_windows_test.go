package browser

import (
	"strings"
	"testing"
)

func TestCleanupPowerShellHasNoConsole(t *testing.T) {
	script := `Add-Type -Name NativeConsole -Namespace BridgeCheck -MemberDefinition '[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern System.IntPtr GetConsoleWindow();'; [BridgeCheck.NativeConsole]::GetConsoleWindow().ToInt64()`
	output, err := hiddenPowerShell(script).CombinedOutput()
	if err != nil {
		t.Fatalf("real PowerShell console probe: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "0" {
		t.Fatalf("cleanup child created a console window: %s", output)
	}
}
