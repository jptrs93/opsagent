-- The stamp of each node's newest published network map. Not a materialisation
-- of the log: the map is a render of the live tables, and the row only lets a
-- restarted primary keep the stamp a node already applied when the render has
-- not changed, so a restart does not look like a map change on every node.
CREATE TABLE IF NOT EXISTS node_netmap (
    node_id      INTEGER PRIMARY KEY,
    derived_seq  INTEGER NOT NULL,
    content_hash TEXT    NOT NULL
);
