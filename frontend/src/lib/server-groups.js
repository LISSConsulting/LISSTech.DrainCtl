/** Presentation-only label used when a hostname does not expose a pool ordinal. */
export const UNGROUPED_RD_SESSION_POOL = 'Ungrouped';

/**
 * Infer an RD Session Pool label from a hostname without treating it as an
 * authoritative discovery source. Fully qualified names are grouped by their
 * hostname stem, because the DNS suffix does not identify a pool.
 *
 * @param {string} host
 * @returns {string}
 */
export function inferRdSessionPool(host) {
    const stem = String(host ?? '')
        .trim()
        .split('.')[0];
    if (!stem) return UNGROUPED_RD_SESSION_POOL;

    // An explicit Remote Desktop host marker is reliable when it occurs at the
    // end of a hostname and carries an ordinal, e.g. POOL-RDSH-01 or MDS-LDC1-RDS8.
    const marked = stem.match(/^(.*[A-Za-z0-9])[-_](?:RDSERVER|SESSIONHOST|RDSH|RDS)[-_]?(\d+)$/i);
    if (marked) return marked[1].toUpperCase();

    // Marker-only hostnames conventionally denote one pool, e.g. RDSH01.
    // A bare ordinal is not enough evidence: SQL01, WEB02, and DC01 are
    // ordinary numbered hosts rather than RD Session Hosts.
    const markerOnly = stem.match(/^(RDSERVER|SESSIONHOST|RDSH|RDS)[-_]?\d+$/i);
    if (markerOnly) return markerOnly[1].toUpperCase();

    return UNGROUPED_RD_SESSION_POOL;
}

/**
 * Partition already-filtered, already-sorted server rows into presentation
 * groups. Pool headers are alphabetized while server order within each pool is
 * retained exactly as supplied.
 *
 * @template T extends {{ host: string }}
 * @param {readonly T[]} servers
 * @returns {{ pool: string, servers: T[] }[]}
 */
export function groupServersByRdSessionPool(servers) {
    const groups = new Map();
    for (const server of servers) {
        const pool = inferRdSessionPool(server.host);
        const members = groups.get(pool);
        if (members) members.push(server);
        else groups.set(pool, [server]);
    }

    return [...groups].sort(([a], [b]) => a.localeCompare(b)).map(([pool, servers]) => ({ pool, servers }));
}
