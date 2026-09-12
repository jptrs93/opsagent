import type { CodeVariant } from './languages';

export const logEventVariants: readonly CodeVariant[] = [
  {
    label: 'Protobuf',
    language: 'protobuf',
    code: `syntax = "proto3";

message LogEvent {
  message Source {
    uint64 node_id = 1;
    uint64 deployment_id = 2;
    uint64 deployment_version = 3;
    uint32 instance_ordinal = 4;
    uint32 stream = 5;  // 1 = stdout, 2 = stderr
    uint64 run = 6;
  }

  message ParsedPayload {
    message IntArray {
      repeated int64 values = 1;
    }
    message FloatArray {
      repeated double values = 1;
    }
    message StringArray {
      repeated string values = 1;
    }

    // Dot-joined keys without a suffix; the map implies the type.
    map<string, int64>       ints          = 1;  // __i
    map<string, double>      floats        = 2;  // __f
    map<string, string>      strings       = 3;  // __s
    map<string, IntArray>    int_arrays    = 4;  // __ai
    map<string, FloatArray>  float_arrays  = 5;  // __af
    map<string, StringArray> string_arrays = 6;  // __as
  }

  fixed64 timestamp = 1;  // Nanoseconds since Unix epoch (UTC)
  uint64 seq = 2;
  Source source = 3;
  bytes payload = 4;  // Raw line; may not be valid UTF-8
  ParsedPayload parsed_payload = 5;  // Set only for valid JSON
}`,
  },
  {
    label: 'Smithy',
    language: 'smithy',
    code: `$version: "2"

namespace opendeploy.logging

// UInt64 exceeds Smithy's signed Long range.
@range(min: 0, max: 18446744073709551615)
bigInteger UInt64

@range(min: 0, max: 4294967295)
long UInt32

structure LogEvent {
    // Nanoseconds since Unix epoch (UTC), not a Smithy timestamp.
    @required
    timestamp: UInt64
    @required
    seq: UInt64
    @required
    source: Source
    // Raw line; may not be valid UTF-8.
    @required
    payload: Blob
    // Set only for valid JSON.
    parsed_payload: ParsedPayload
}

structure Source {
    @required
    node_id: UInt64
    @required
    deployment_id: UInt64
    @required
    deployment_version: UInt64
    @required
    instance_ordinal: UInt32
    // 1 = stdout, 2 = stderr
    @required
    stream: UInt32
    @required
    run: UInt64
}

structure ParsedPayload {
    // Dot-joined keys without a suffix; the map implies the type.
    @required
    ints: IntMap                 // __i
    @required
    floats: FloatMap             // __f
    @required
    strings: StringMap           // __s
    @required
    int_arrays: IntArrayMap      // __ai
    @required
    float_arrays: FloatArrayMap  // __af
    @required
    string_arrays: StringArrayMap // __as
}

map IntMap {
    key: String
    value: Long
}

map FloatMap {
    key: String
    value: Double
}

map StringMap {
    key: String
    value: String
}

map IntArrayMap {
    key: String
    value: IntArray
}

map FloatArrayMap {
    key: String
    value: FloatArray
}

map StringArrayMap {
    key: String
    value: StringArray
}

list IntArray {
    member: Long
}

list FloatArray {
    member: Double
}

list StringArray {
    member: String
}`,
  },
  {
    label: 'TypeScript',
    language: 'typescript',
    code: `// Use bigint for 64-bit integers to preserve precision.
interface LogEvent {
  timestamp: bigint; // Nanoseconds since Unix epoch (UTC).
  seq: bigint;
  source: Source;
  payload: Uint8Array; // Raw line; may not be valid UTF-8.
  parsed_payload?: ParsedPayload; // Set only for valid JSON.
}

interface Source {
  node_id: bigint;
  deployment_id: bigint;
  deployment_version: bigint;
  instance_ordinal: number;
  stream: number; // 1 = stdout, 2 = stderr
  run: bigint;
}

interface ParsedPayload {
  // Dot-joined keys without a suffix; the map implies the type.
  ints: Record<string, bigint>;            // __i
  floats: Record<string, number>;          // __f
  strings: Record<string, string>;         // __s
  int_arrays: Record<string, bigint[]>;    // __ai
  float_arrays: Record<string, number[]>;  // __af
  string_arrays: Record<string, string[]>; // __as
}`,
  },
];
