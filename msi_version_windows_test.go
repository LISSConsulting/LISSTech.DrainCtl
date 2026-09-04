//go:build windows

package drainctl

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestMSIVersionUsesSchemaEpoch(t *testing.T) {
	t.Setenv("MSI_REVISION", "7")

	appOutput, err := exec.Command("pwsh", "-NoProfile", "-File", "scripts/version.ps1").CombinedOutput()
	if err != nil {
		t.Fatalf("version.ps1: %v\n%s", err, appOutput)
	}
	var appYear, appMonth, appBuild int
	if n, scanErr := fmt.Sscanf(strings.TrimSpace(string(appOutput)), "%d.%d.%d", &appYear, &appMonth, &appBuild); scanErr != nil || n != 3 {
		t.Fatalf("parse app version %q: matched %d components, err=%v", appOutput, n, scanErr)
	}

	msiOutput, err := exec.Command("pwsh", "-NoProfile", "-File", "scripts/msi-version.ps1").CombinedOutput()
	if err != nil {
		t.Fatalf("msi-version.ps1: %v\n%s", err, msiOutput)
	}
	var msiMajor, msiMonth, msiBuild int
	if n, scanErr := fmt.Sscanf(strings.TrimSpace(string(msiOutput)), "%d.%d.%d", &msiMajor, &msiMonth, &msiBuild); scanErr != nil || n != 3 {
		t.Fatalf("parse MSI version %q: matched %d components, err=%v", msiOutput, n, scanErr)
	}

	if msiMajor != 100+appYear || msiMonth != appMonth || msiBuild != appBuild*100+7 {
		t.Errorf("MSI version = %d.%d.%d, want %d.%d.%d", msiMajor, msiMonth, msiBuild, 100+appYear, appMonth, appBuild*100+7)
	}
}
