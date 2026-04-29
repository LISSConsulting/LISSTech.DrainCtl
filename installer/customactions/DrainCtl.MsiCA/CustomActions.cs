using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using WixToolset.Dtf.WindowsInstaller;

namespace LISSTech.DrainCtl.MsiCA
{
    /// <summary>
    /// Managed custom actions for the LISSTech DrainCtl MSI. Replaces a
    /// retired Go-based CA (ApplyInstallerConfig) plus four
    /// WixQuietExec wrappers around wevtutil and drainctl.exe. Every CA
    /// streams stdout/stderr into the MSI log via Session.Log so failures
    /// are diagnosable without re-running the install with /l*v.
    /// </summary>
    public static class CustomActions
    {
        // CustomActionData payload format: one "Key=Value" per line, LF-separated.
        // Values do not need to be escaped — the keys we use never contain '=' or
        // newlines on the value side (paths, integers, identifiers, "true"/"false").
        private static Dictionary<string, string> ParsePayload(string payload)
        {
            // Case-sensitive — single-letter payload keys use case to distinguish
            // 'p' (PollInterval) from 'P' (DashboardPort), 'g' from 'G'.
            var result = new Dictionary<string, string>(StringComparer.Ordinal);
            if (string.IsNullOrEmpty(payload))
            {
                return result;
            }
            foreach (var rawLine in payload.Split('\n'))
            {
                var line = rawLine.Trim('\r').Trim();
                if (line.Length == 0)
                {
                    continue;
                }
                var idx = line.IndexOf('=');
                if (idx <= 0)
                {
                    continue;
                }
                result[line.Substring(0, idx)] = line.Substring(idx + 1);
            }
            return result;
        }

        // Run an executable, capturing stdout and stderr into the MSI log.
        // Returns the process exit code, or -1 on launch failure.
        private static int RunCommand(Session session, string exe, IList<string> args)
        {
            var quoted = new List<string>(args.Count);
            foreach (var a in args)
            {
                quoted.Add(QuoteArg(a));
            }
            var cmd = string.Join(" ", quoted);
            session.Log("DrainCtl.MsiCA: exec: \"{0}\" {1}", exe, cmd);

            try
            {
                var psi = new ProcessStartInfo
                {
                    FileName = exe,
                    Arguments = cmd,
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                };
                using (var p = Process.Start(psi))
                {
                    var stdout = p.StandardOutput.ReadToEnd();
                    var stderr = p.StandardError.ReadToEnd();
                    p.WaitForExit();
                    if (!string.IsNullOrEmpty(stdout))
                    {
                        session.Log("DrainCtl.MsiCA: stdout: {0}", stdout.TrimEnd());
                    }
                    if (!string.IsNullOrEmpty(stderr))
                    {
                        session.Log("DrainCtl.MsiCA: stderr: {0}", stderr.TrimEnd());
                    }
                    session.Log("DrainCtl.MsiCA: exit: {0}", p.ExitCode);
                    return p.ExitCode;
                }
            }
            catch (Exception ex)
            {
                session.Log("DrainCtl.MsiCA: launch failed: {0}", ex);
                return -1;
            }
        }

        // Quote an argument per the CommandLineToArgvW rules used by Process.Start
        // when UseShellExecute=false. Empty string → "" (preserves the empty arg);
        // arg with whitespace or quotes → wrap in double quotes and escape.
        private static string QuoteArg(string a)
        {
            if (string.IsNullOrEmpty(a))
            {
                return "\"\"";
            }
            if (a.IndexOfAny(new[] { ' ', '\t', '"' }) < 0)
            {
                return a;
            }
            return "\"" + a.Replace("\\", "\\\\").Replace("\"", "\\\"") + "\"";
        }

        /// <summary>
        /// Apply machine-level settings collected by the MSI wizard to config.json.
        /// Reads CustomActionData as Key=Value lines, maps to drainctl.exe configure
        /// flags, and execs the binary so config persistence stays single-sourced
        /// in Go.
        /// </summary>
        [CustomAction]
        public static ActionResult ApplyInstallerConfig(Session session)
        {
            var values = ParsePayload(session["CustomActionData"]);

            // Payload uses single-letter keys to keep the source string under
            // the 255-char CustomAction.Target column cap (ICE03). Keys mirror
            // the format the retired Go CA used.
            string binFolder;
            if (!values.TryGetValue("b", out binFolder) || string.IsNullOrEmpty(binFolder))
            {
                session.Log("DrainCtl.MsiCA: ApplyInstallerConfig: BinFolder (key 'b') missing from CustomActionData");
                return ActionResult.Failure;
            }
            var drainctlExe = Path.Combine(binFolder, "drainctl.exe");
            if (!File.Exists(drainctlExe))
            {
                session.Log("DrainCtl.MsiCA: ApplyInstallerConfig: drainctl.exe not found at {0}", drainctlExe);
                return ActionResult.Failure;
            }

            var args = new List<string> { "configure" };
            // Map single-letter payload keys → drainctl configure flags. Only emit
            // a flag when the value is non-empty (matches the Go CA's pointer-set
            // semantics — absent keys do not override config).
            AddFlag(args, values, "m", "--mode");
            AddFlag(args, values, "u", "--dashboard-url");
            AddFlag(args, values, "g", "--grace-period");
            AddFlag(args, values, "p", "--poll-interval");
            AddFlag(args, values, "s", "--session-warning-threshold");
            AddFlag(args, values, "P", "--dashboard-port");
            AddFlag(args, values, "G", "--dashboard-group");
            AddBoolFlag(args, values, "e", "--perf-enabled");
            AddBoolFlag(args, values, "r", "--perf-rfx");
            AddFlag(args, values, "f", "--log-file-level");
            AddFlag(args, values, "v", "--log-event-level");
            AddBoolFlag(args, values, "d", "--dashboard-only");

            var rc = RunCommand(session, drainctlExe, args);
            return rc == 0 ? ActionResult.Success : ActionResult.Failure;
        }

        private static void AddFlag(List<string> args, Dictionary<string, string> values, string key, string flag)
        {
            string val;
            if (values.TryGetValue(key, out val) && !string.IsNullOrEmpty(val))
            {
                args.Add(flag + "=" + val);
            }
        }

        // Normalize MSI-style boolean values ("1"/"0") to the "true"/"false" tokens
        // cobra's BoolVar accepts, so the same flag works whether the property
        // came from a checkbox or an explicit unattended-install argument.
        private static void AddBoolFlag(List<string> args, Dictionary<string, string> values, string key, string flag)
        {
            string val;
            if (!values.TryGetValue(key, out val) || string.IsNullOrEmpty(val))
            {
                return;
            }
            string norm;
            switch (val.Trim().ToLowerInvariant())
            {
                case "1":
                case "true":
                case "yes":
                    norm = "true";
                    break;
                case "0":
                case "false":
                case "no":
                    norm = "false";
                    break;
                default:
                    norm = val;
                    break;
            }
            args.Add(flag + "=" + norm);
        }

        /// <summary>
        /// Register the DrainCtl ETW manifest. CustomActionData is the absolute
        /// path to drainctl.man.
        /// </summary>
        [CustomAction]
        public static ActionResult RegisterEtwProvider(Session session)
        {
            return RunWevtutil(session, "im");
        }

        /// <summary>
        /// Unregister any stale prior registration of the manifest (idempotent
        /// on fresh installs — wevtutil reports 5 / "not found" which we log and
        /// ignore).
        /// </summary>
        [CustomAction]
        public static ActionResult CleanEtwProvider(Session session)
        {
            return RunWevtutil(session, "um");
        }

        /// <summary>
        /// Unregister the manifest on uninstall. Conditioned in WiX so the
        /// upgrade-driven removal of the prior product does not run this and
        /// clobber the freshly-registered manifest.
        /// </summary>
        [CustomAction]
        public static ActionResult UnregisterEtwProvider(Session session)
        {
            return RunWevtutil(session, "um");
        }

        private static ActionResult RunWevtutil(Session session, string verb)
        {
            var manifest = (session["CustomActionData"] ?? string.Empty).Trim();
            if (manifest.Length == 0)
            {
                session.Log("DrainCtl.MsiCA: wevtutil {0}: empty CustomActionData", verb);
                return ActionResult.Success;
            }
            if (verb == "im" && !File.Exists(manifest))
            {
                session.Log("DrainCtl.MsiCA: wevtutil im: manifest not found at {0}", manifest);
                return ActionResult.Success;
            }
            var sysFolder = Environment.GetFolderPath(Environment.SpecialFolder.System);
            var wevtutil = Path.Combine(sysFolder, "wevtutil.exe");
            var rc = RunCommand(session, wevtutil, new[] { verb, manifest });
            // Don't fail install on wevtutil error — the manifest can be
            // registered manually post-install. The exec output is in the MSI
            // log so an operator can see exactly why it failed.
            if (rc != 0)
            {
                session.Log("DrainCtl.MsiCA: wevtutil {0} returned {1}; continuing", verb, rc);
            }
            return ActionResult.Success;
        }

        /// <summary>
        /// Add the service account to the Event Log Readers group so the
        /// service can subscribe to event channels. CustomActionData is the
        /// absolute path to drainctl.exe.
        /// </summary>
        [CustomAction]
        public static ActionResult AddEventLogReaders(Session session)
        {
            var exe = (session["CustomActionData"] ?? string.Empty).Trim();
            if (exe.Length == 0)
            {
                session.Log("DrainCtl.MsiCA: AddEventLogReaders: empty CustomActionData");
                return ActionResult.Failure;
            }
            if (!File.Exists(exe))
            {
                session.Log("DrainCtl.MsiCA: AddEventLogReaders: drainctl.exe not found at {0}", exe);
                return ActionResult.Failure;
            }
            var rc = RunCommand(session, exe, new[] { "service", "grant-eventlog" });
            return rc == 0 ? ActionResult.Success : ActionResult.Failure;
        }
    }
}
