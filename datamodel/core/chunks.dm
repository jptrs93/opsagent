type ChunkLocation {
    local@1: bool
    archive@2: ?(type S3Archive@2 {
    })
    laws {
        held_somewhere {
            assert local || archive is present
        }
    }
}

type LogChunk@24 root {
    id@1: u64 [key]
    deployment_id@2: u64
    node_id@3: Node.id
    day@4: u32
    level@5: u32 [max = 2, min = 1]
    seq@6: i64
    min_time@7: i64
    max_time@8: i64
    row_count@9: u64
    size_bytes@10: u64
    sha256@11: [32]u8
    location@12: ChunkLocation
    laws {
        log_chunk_file {
            unique (node_id, deployment_id, day, seq)
        }
        log_chunk_time_range {
            assert min_time <= max_time
        }
    }
}

type DeploymentMetricsChunk@25 root {
    id@1: u64 [key]
    deployment_id@2: u64
    node_id@3: Node.id
    day@4: u32
    seq@5: u32 [min = 1]
    min_time@6: i64
    max_time@7: i64
    row_count@8: u64
    size_bytes@9: u64
    sha256@10: [32]u8
    location@11: ChunkLocation
    laws {
        deployment_metrics_chunk_file {
            unique (node_id, deployment_id, day, seq)
        }
        deployment_metrics_chunk_time_range {
            assert min_time <= max_time
        }
    }
}

type NodeMetricsChunk@26 root {
    id@1: u64 [key]
    node_id@2: Node.id
    day@3: u32
    seq@4: u32 [min = 1]
    min_time@5: i64
    max_time@6: i64
    row_count@7: u64
    size_bytes@8: u64
    sha256@9: [32]u8
    location@10: ChunkLocation
    laws {
        node_metrics_chunk_file {
            unique (node_id, day, seq)
        }
        node_metrics_chunk_time_range {
            assert min_time <= max_time
        }
    }
}
