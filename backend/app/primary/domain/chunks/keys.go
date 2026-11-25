package chunks

import (
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func dayName(day uint32) string {
	return time.Unix(int64(day)*86400, 0).UTC().Format("20060102")
}

func ObjectKey(r *pq.ChunkRow) string {
	switch r.Kind {
	case apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK:
		return fmt.Sprintf("logs/%d/%s/L%d_%d-%d_n%d_%d.parquet", r.DeploymentID, dayName(r.Day), r.Level, r.MinTime/1e6, r.MaxTime/1e6, r.NodeID, r.FileSeq)
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK:
		return fmt.Sprintf("metrics/%s/d%d_n%d_%d.parquet", dayName(r.Day), r.DeploymentID, r.NodeID, r.FileSeq)
	}
	return fmt.Sprintf("metrics/node/%s/n%d_%d.parquet", dayName(r.Day), r.NodeID, r.FileSeq)
}
