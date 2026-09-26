/** Presentation-only label used when a server has no discovered collection. */
export const UNGROUPED_RD_SESSION_COLLECTION = 'Ungrouped';

/**
 * Return the authoritative RD Session Collection label for a server. Collection
 * membership is discovered by the backend; hostnames are not a source of truth.
 *
 * @param {{ rd_session_collection?: string|null }} server
 * @returns {string}
 */
export function rdSessionCollectionFor(server) {
    const collection = String(server?.rd_session_collection ?? '').trim();
    return collection || UNGROUPED_RD_SESSION_COLLECTION;
}

/**
 * Partition already-filtered, already-sorted server rows into presentation
 * groups. Collection headers are alphabetized while server order within each
 * collection is retained exactly as supplied.
 *
 * @template T extends {{ rd_session_collection?: string|null }}
 * @param {readonly T[]} servers
 * @returns {{ collection: string, servers: T[] }[]}
 */
export function groupServersByRdSessionCollection(servers) {
    const groups = new Map();
    for (const server of servers) {
        const collection = rdSessionCollectionFor(server);
        const members = groups.get(collection);
        if (members) members.push(server);
        else groups.set(collection, [server]);
    }

    return [...groups].sort(([a], [b]) => a.localeCompare(b)).map(([collection, servers]) => ({ collection, servers }));
}
