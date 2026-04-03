//go:build windows

package drainctl

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RunAuditSetup configures registry auditing (auditpol + SACL) so that
// Windows records Event ID 4657 whenever TSServerDrainMode is modified.
// Must be run elevated (Administrator or SYSTEM).
func RunAuditSetup(log LogFunc) error {
	if log == nil {
		log = DiscardLogger()
	}

	log(LvlINF, "cmd=audit-setup", "action=configure_registry_sacl")

	// ── Pre-flight checks ──────────────────────────────────────────────
	domainJoined := isDomainJoined()
	if domainJoined {
		log(LvlWRN, "domain_joined=true",
			`msg="Local auditpol settings may be overwritten by Group Policy refresh (~90 min)"`)
		log(LvlWRN, "gpo_path=\"Computer Configuration > Policies > Windows Settings > Security Settings > Advanced Audit Policy Configuration > Object Access > Audit Registry > Success\"")
	}

	checkSubcategoryOverride(log)
	checkSecurityLogSize(log)

	// ── Step 1: Enable registry audit policy ───────────────────────────
	log(LvlINF, "step=1/2", "action=enable_audit_policy", "subcategory=Registry")
	out, err := exec.Command("auditpol", "/set",
		"/subcategory:Registry",
		"/success:enable",
		"/failure:enable",
	).CombinedOutput()
	if err != nil {
		LogMsg(log, LvlERR, "auditpol failed", fmt.Sprintf("error=%q", err), fmt.Sprintf("output=%q", strings.TrimSpace(string(out))))
		return fmt.Errorf("auditpol: %w", err)
	}
	log(LvlOK, "step=1/2", "result=audit_policy_enabled")

	if domainJoined {
		log(LvlWRN, "step=1/2",
			`msg="Configure the equivalent GPO setting to make this persistent across Group Policy refreshes"`)
	}

	// ── Step 2: Set SACL on the Terminal Server registry key ───────────
	log(LvlINF, "step=2/2", "action=set_registry_sacl", fmt.Sprintf("key=HKLM\\%s", RegPath))

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
		LogMsg(log, LvlERR, "SACL configuration failed", fmt.Sprintf("error=%q", err), fmt.Sprintf("output=%q", strings.TrimSpace(string(out))))
		return fmt.Errorf("set SACL: %w", err)
	}
	log(LvlOK, "step=2/2", "result=sacl_configured")

	log(LvlOK,
		"audit_setup=complete",
		`msg="Registry modifications to TSServerDrainMode will now be attributed to the acting user"`,
	)
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

func checkSubcategoryOverride(log LogFunc) {
	out, err := exec.Command("reg", "query",
		`HKLM\System\CurrentControlSet\Control\Lsa`,
		"/v", "SCENoApplyLegacyAuditPolicy",
	).Output()
	if err != nil {
		log(LvlWRN, `msg="Could not check 'Force subcategory override' setting"`,
			fmt.Sprintf("error=%q", err))
		log(LvlWRN, `msg="Verify: Security Options > 'Audit: Force audit policy subcategory settings to override audit category settings' is Enabled"`)
		return
	}

	if !strings.Contains(string(out), "0x1") {
		log(LvlWRN, "subcategory_override=disabled",
			`msg="The legacy 'Audit object access' category may conflict with subcategory settings"`)
		log(LvlWRN, `msg="Enable: Security Options > 'Audit: Force audit policy subcategory settings to override audit category settings'"`)
	}
}

func checkSecurityLogSize(log LogFunc) {
	out, err := exec.Command("wevtutil", "gl", "Security").Output()
	if err != nil {
		log(LvlWRN, `msg="Could not query Security Event Log size"`, fmt.Sprintf("error=%q", err))
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
			log(LvlINF, fmt.Sprintf("security_log_max_size=%dMB", sizeMB))
			if size < 67108864 {
				log(LvlWRN, fmt.Sprintf("security_log_max_size=%dMB", sizeMB),
					`msg="Recommend 64-128 MB to prevent Event ID 4657 records from being rotated out between polling intervals"`)
			}
			return
		}
	}
}
