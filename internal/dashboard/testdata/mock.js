(function () {
    var now = new Date().toISOString();

    var perfHealthy = {
        cpu_pct: 42.5,
        mem_avail_mb: 8192,
        mem_total_mb: 16384,
        pages_sec: 12.0,
        disk_queue: 0.3,
        tcp_retrans_sec: 0.5,
        input_delay_p50_ms: 8.2,
        input_delay_p95_ms: 22.4,
        input_delay_max_ms: 45.0,
    };
    var perfWarn = {
        cpu_pct: 73.1,
        mem_avail_mb: 3276,
        mem_total_mb: 16384,
        pages_sec: 120.0,
        disk_queue: 1.8,
        tcp_retrans_sec: 2.1,
        input_delay_p50_ms: 38.0,
        input_delay_p95_ms: 62.5,
        input_delay_max_ms: 95.0,
    };

    // mk returns a ServerView-shaped object matching the Go backend's
    // ServerView struct (flattened host/status/sessions/perf at top level).
    function mk(name, status, by, sessions, perf) {
        var registered = "2026-03-01T00:00:00Z";
        if (!status) {
            return {
                host: name,
                status: "off",
                drain_mode: "ALLOW_ALL_CONNECTIONS",
                sessions: 0,
                version: "",
                registered_at: registered,
                last_seen: new Date(Date.now() - 600000).toISOString(),
                changed_by: "",
                grace_deadline: null,
                perf: null,
            };
        }
        var graceDeadline = null;
        if (status === "grace") {
            // Deadline ~45 minutes from now.
            graceDeadline = new Date(Date.now() + 45 * 60 * 1000).toISOString();
        }
        var drainMode = status === "ok"
            ? "ALLOW_ALL_CONNECTIONS"
            : "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS";
        return {
            host: name,
            status: status,
            drain_mode: drainMode,
            sessions: sessions || 0,
            version: "26.100.9",
            registered_at: registered,
            last_seen: now,
            changed_by: by || "",
            grace_deadline: graceDeadline,
            perf: perf || null,
        };
    }

    var patterns = [
        ["ok", "ok", "ok", "ok"],
        ["ok", "grace", "ok", "ok"],
        ["ok", "grace", "grace", "ok"],
        ["ok", "alert", "alert", "ok"],
        ["ok", "alert", "alert", null],
        ["ok", "grace", "alert", null],
        ["ok", "ok", "alert", null],
        ["ok", "ok", "alert", "ok"],
        ["ok", "ok", "grace", "ok"],
        ["ok", "grace", "ok", "ok"],
        ["ok", "alert", "ok", "ok"],
        ["ok", "alert", "grace", "ok"],
        ["ok", "alert", "alert", null],
        ["ok", "grace", "alert", null],
        ["ok", "grace", "alert", "ok"],
        ["ok", "ok", "alert", "ok"],
        ["ok", "grace", "alert", "ok"],
    ];
    for (var i = 0; i < patterns.length; i++) {
        var p = patterns[i];
        render([
            mk("RDSH01", p[0], "",                  12, perfHealthy),
            mk("RDSH02", p[1], "admin@contoso",     20, perfWarn),
            mk("RDSH03", p[2], "svc-rds@contoso",   24, perfWarn),
            mk("RDSH04", p[3], "",                   0, null),
        ]);
    }
    render([
        mk("RDSH01", "ok",    "",                  12, perfHealthy),
        mk("RDSH02", "grace", "admin@contoso",     20, perfWarn),
        mk("RDSH03", "alert", "svc-rds@contoso",   24, null),
        mk("RDSH04", null,    null,                  0, null),
    ]);
})();
