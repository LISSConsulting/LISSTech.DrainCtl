//go:build windows

package drainctl

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// RunAuditSetup configures registry auditing (auditpol + SACL) so that
// Windows records Event ID 4657 whenever TSServerDrainMode is modified.
// Must be run elevated (Administrator or SYSTEM).
func RunAuditSetup() error {
	slog.Info("", "cmd", "audit-setup", "action", "configure_registry_sacl")

	// ── Pre-flight checks ──────────────────────────────────────────────
	domainJoined := isDomainJoined()
	if domainJoined {
		slog.Warn("", "domain_joined", true,
			"msg", "Local auditpol settings may be overwritten by Group Policy refresh (~90 min)")
		slog.Warn("gpo_path=\"Computer Configuration > Policies > Windows Settings > Security Settings > Advanced Audit Policy Configuration > Object Access > Audit Registry > Success\"")
	}

	checkSubcategoryOverride()
	checkSecurityLogSize()

	// ── Step 1: Enable registry audit policy ───────────────────────────
	slog.Info("", "step", "1/2", "action", "enable_audit_policy", "subcategory", "Registry")
	out, err := exec.Command("auditpol", "/set",
		"/subcategory:Registry",
		"/success:enable",
		"/failure:enable",
	).CombinedOutput()
	if err != nil {
		slog.Error("auditpol failed", "error", err, "output", strings.TrimSpace(string(out)))
		return fmt.Errorf("auditpol: %w", err)
	}
	slog.Info("step=1/2 result=audit_policy_enabled")

	if domainJoined {
		slog.Warn("", "step", "1/2",
			"msg", "Configure the equivalent GPO setting to make this persistent across Group Policy refreshes")
	}

	// ── Step 2: Set SACL on the Terminal Server registry key ───────────
	slog.Info("", "step", "2/2", "action", "set_registry_sacl", "key", `HKLM\`+RegPath)

	ps := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$key = 'HKLM:\%s'
$acl = Get-Acl $key -Audit
$rule = New-Object System.Security.AccessControl.RegistryAuditRule(
    'Everyone',
    'SetValue',
    'None',
    'None',
    'Success,Failure'
)
$acl.AddAuditRule($rule)
Set-Acl $key $acl
Write-Output 'SACL configured successfully'
`, RegPath)

	out, err = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		slog.Error("SACL configuration failed", "error", err, "output", strings.TrimSpace(string(out)))
		return fmt.Errorf("set SACL: %w", err)
	}
	slog.Info("step=2/2 result=sacl_configured")

	slog.Info("audit_setup=complete msg=\"Registry modifications to TSServerDrainMode and fDenyTSConnections will now be attributed to the acting user\"")
	return nil
}

func isDomainJoined() bool {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance Win32_ComputerSystem).PartOfDomain",
	).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "True"
}

func checkSubcategoryOverride() {
	out, err := exec.Command("reg", "query",
		`HKLM\System\CurrentControlSet\Control\Lsa`,
		"/v", "SCENoApplyLegacyAuditPolicy",
	).Output()
	if err != nil {
		slog.Warn("Could not check 'Force subcategory override' setting", "error", err)
		slog.Warn("Verify: Security Options > 'Audit: Force audit policy subcategory settings to override audit category settings' is Enabled")
		return
	}

	if !strings.Contains(string(out), "0x1") {
		slog.Warn("subcategory_override=disabled",
			"msg", "The legacy 'Audit object access' category may conflict with subcategory settings")
		slog.Warn("Enable: Security Options > 'Audit: Force audit policy subcategory settings to override audit category settings'")
	}
}

func checkSecurityLogSize() {
	out, err := exec.Command("wevtutil", "gl", "Security").Output()
	if err != nil {
		slog.Warn("Could not query Security Event Log size", "error", err)
		return
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "maxsize:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			sizeStr := strings.TrimSpace(parts[1])
			size, err := strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				continue
			}
			sizeMB := size / (1024 * 1024)
			slog.Info("", "security_log_max_size_mb", sizeMB)
			if size < 67108864 {
				slog.Warn("Security log size below recommendation",
					"security_log_max_size_mb", sizeMB,
					"msg", "Recommend 64-128 MB to prevent Event ID 4657 records from being rotated out between polling intervals")
			}
			return
		}
	}
}
