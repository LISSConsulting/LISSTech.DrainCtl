(function () {
    var now = new Date().toISOString();
    function mk(name, status, by, sess) {
        if (!status)
            return {
                hostname: name,
                registered_at: "2026-03-01T00:00:00Z",
                last_seen: new Date(Date.now() - 600000).toISOString(),
                last_result: null,
            };
        return {
            hostname: name,
            registered_at: "2026-03-01T00:00:00Z",
            last_seen: now,
            last_result: {
                version: "26.94.1",
                timestamp: now,
                host: name,
                drain_mode: status === "Healthy" ? "ALLOW_ALL_CONNECTIONS" : "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS",
                drain_mode_value: status === "Healthy" ? 0 : 1,
                state_duration_seconds: status === "Alert" ? 7200 : status === "Grace" ? 1200 : 3600,
                grace_period_seconds: 3600,
                status: status,
                connections_allowed: status === "Healthy",
                changed_by: by || "",
                sessions: sess,
                exit_code: status === "Alert" ? 1 : 0,
            },
        };
    }
    var s1 = {
        active_sessions: 12,
        disconnected_sessions: 3,
        total_sessions: 15,
        max_sessions: 25,
        utilization_pct: 60,
    };
    var s2 = {
        active_sessions: 20,
        disconnected_sessions: 2,
        total_sessions: 22,
        max_sessions: 25,
        utilization_pct: 88,
    };
    var s3 = {
        active_sessions: 24,
        disconnected_sessions: 1,
        total_sessions: 25,
        max_sessions: 25,
        utilization_pct: 100,
    };
    var s4 = { active_sessions: 0, disconnected_sessions: 0, total_sessions: 0, max_sessions: 25, utilization_pct: 0 };
    var patterns = [
        ["Healthy", "Healthy", "Healthy", "Healthy"],
        ["Healthy", "Grace", "Healthy", "Healthy"],
        ["Healthy", "Grace", "Grace", "Healthy"],
        ["Healthy", "Alert", "Alert", "Healthy"],
        ["Healthy", "Alert", "Alert", null],
        ["Healthy", "Grace", "Alert", null],
        ["Healthy", "Healthy", "Alert", null],
        ["Healthy", "Healthy", "Alert", "Healthy"],
        ["Healthy", "Healthy", "Grace", "Healthy"],
        ["Healthy", "Grace", "Healthy", "Healthy"],
        ["Healthy", "Alert", "Healthy", "Healthy"],
        ["Healthy", "Alert", "Grace", "Healthy"],
        ["Healthy", "Alert", "Alert", null],
        ["Healthy", "Grace", "Alert", null],
        ["Healthy", "Grace", "Alert", "Healthy"],
        ["Healthy", "Healthy", "Alert", "Healthy"],
        ["Healthy", "Grace", "Alert", "Healthy"],
    ];
    for (var i = 0; i < patterns.length; i++) {
        var p = patterns[i];
        render([
            mk("RDSH01", p[0], "", s1),
            mk("RDSH02", p[1], "admin@contoso", s2),
            mk("RDSH03", p[2], "svc-rds@contoso", s3),
            mk("RDSH04", p[3], "", s4),
        ]);
    }
    render([
        mk("RDSH01", "Healthy", "", s1),
        mk("RDSH02", "Grace", "admin@contoso", s2),
        mk("RDSH03", "Alert", "svc-rds@contoso", s3),
        mk("RDSH04", null, null, null),
    ]);
})();
