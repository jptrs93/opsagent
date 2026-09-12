export const eventShape = String.raw`{
  "timestamp": 1789084800000000000,
  "seq": 42,
  "source": {
    "node_id": 7,
    "deployment_id": 1204,
    "deployment_version": 12,
    "instance_ordinal": 0,
    "stream": 1,
    "run": 3
  },
  "payload": "{\"level\":\"info\",\"msg\":\"request complete\",\"status\":200}\n",
  "parsed_payload": {
    "ints": {
      "status": 200
    },
    "strings": {
      "level": "info",
      "msg": "request complete"
    }
  }
}`;

export const walLayout = `<LogWALDir>/<deployment_id>/
  20260911_0000.wal   # 00:00 <= event time < 00:30 UTC
  20260911_0030.wal   # 00:30 <= event time < 01:00 UTC
  20260911_0100.wal   # 01:00 <= event time < 01:30 UTC`;

export const droppedMarker = `opendeploy: dropped 412 log lines between 2026-09-11T03:12:08Z and 2026-09-11T03:14:51Z: write: no space left on device`;

export const archiveLayout = `<LogArchiveDir>/
  logdb.sqlite
  <deployment_id>/
    <YYYYMMDD>/
      L1_<minUnixMs>-<maxUnixMs>_n<node>_<fileSeq>.parquet
      L2_<minUnixMs>-<maxUnixMs>_n<node>_<fileSeq>.parquet`;

export const queryExamples = `level:error "pool exhausted" status:500 err:*
request.status:503 duration_ms>=100 -trace_id:* version:12`;
